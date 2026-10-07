package web

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	ginMw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v6"
	gcrypto "github.com/Laisky/go-utils/v6/crypto"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	blogCtl "github.com/Laisky/laisky-blog-graphql/internal/web/blog/controller"
	blogDao "github.com/Laisky/laisky-blog-graphql/internal/web/blog/dao"
	blogModel "github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	blogSvc "github.com/Laisky/laisky-blog-graphql/internal/web/blog/service"
	"github.com/Laisky/laisky-blog-graphql/library/auth"
)

const (
	// ssoE2EOrigin is the WebAuthn origin the harness configures for the SSO site.
	ssoE2EOrigin = "https://sso.example.test"
	// ssoE2ERPID is the WebAuthn relying-party ID matching ssoE2EOrigin.
	ssoE2ERPID = "sso.example.test"
	// ssoE2ESecret signs passkey sessions and derives the SSO signing key.
	ssoE2ESecret = "sso-e2e-secret-for-tests-only-0123456789"
)

// ssoGlobalsMu serializes tests that mutate process-wide SSO configuration.
var ssoGlobalsMu sync.Mutex

// ssoMongoDB adapts one ephemeral database to the blog DAO database contract.
type ssoMongoDB struct{ db *mongo.Database }

// Close leaves client cleanup to the owning test.
func (d ssoMongoDB) Close(context.Context) error { return nil }

// GetCol returns a collection from the ephemeral database.
func (d ssoMongoDB) GetCol(name string) *mongo.Collection { return d.db.Collection(name) }

// DB returns another database on the same client.
func (d ssoMongoDB) DB(name string) *mongo.Database { return d.db.Client().Database(name) }

// CurrentDB returns the ephemeral database.
func (d ssoMongoDB) CurrentDB() *mongo.Database { return d.db }

// ssoE2E is a real GraphQL SSO stack (gin + generated schema + blog
// controller/service/DAO) backed by an ephemeral MongoDB database.
type ssoE2E struct {
	db     *mongo.Database
	svc    *blogSvc.Blog
	engine *gin.Engine
}

// newSSOE2E builds the stack. It skips when no MongoDB URI is configured; the
// Mongo contract CI job always provides one.
func newSSOE2E(t *testing.T) *ssoE2E {
	t.Helper()
	uri := os.Getenv("MONGO_QUERY_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_QUERY_TEST_URI is required; the Mongo contract CI job always sets it")
	}

	ssoGlobalsMu.Lock()
	t.Cleanup(ssoGlobalsMu.Unlock)
	restoreSSOConfig(t, map[string]any{
		"settings.secret":                       ssoE2ESecret,
		"settings.web.webauthn.origin":          ssoE2EOrigin,
		"settings.web.webauthn.rp_id":           ssoE2ERPID,
		"settings.web.webauthn.rp_display_name": "SSO E2E",
		"settings.web.sites":                    map[string]any{},
		"settings.web.turnstile.secret_key":     "",
		"settings.web.sso_jwt.private_key":      "",
	})
	previousAuth := auth.Instance
	require.NoError(t, auth.Initialize([]byte(ssoE2ESecret)))
	t.Cleanup(func() { auth.Instance = previousAuth })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	require.NoError(t, err)
	db := client.Database("sso_e2e_" + primitive.NewObjectID().Hex())
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		require.NoError(t, db.Drop(cleanup))
		require.NoError(t, client.Disconnect(cleanup))
	})
	require.NoError(t, client.Ping(ctx, nil))

	svc, err := blogSvc.New(ctx, glog.Shared, blogDao.New(glog.Shared, ssoMongoDB{db: db}, nil), nil)
	require.NoError(t, err)
	resolver := newContractResolver(ResolverArgs{BlogSvc: svc, BlogCtl: blogCtl.New(svc)})

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	server := handler.NewDefaultServer(NewExecutableSchema(Config{Resolvers: resolver}))
	engine.Any("/query", ginMw.FromStd(server.ServeHTTP))

	return &ssoE2E{db: db, svc: svc, engine: engine}
}

// restoreSSOConfig applies config overrides and restores the previous values.
func restoreSSOConfig(t *testing.T, overrides map[string]any) {
	t.Helper()
	for key, value := range overrides {
		previous := gconfig.Shared.Get(key)
		gconfig.Shared.Set(key, value)
		t.Cleanup(func() { gconfig.Shared.Set(key, previous) })
	}
}

// graphql executes one operation through gin, exactly like the production route.
func (e *ssoE2E) graphql(t *testing.T, bearer string, query string, variables map[string]any) graphQLResponse {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/query", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "203.0.113.10:5000"
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	recorder := httptest.NewRecorder()
	e.engine.ServeHTTP(recorder, request)
	var response graphQLResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	return response
}

// stringField decodes one string field from an object-valued GraphQL result.
func stringField(t *testing.T, response graphQLResponse, root string, field string) string {
	t.Helper()
	require.Empty(t, response.Errors, "unexpected GraphQL errors: %+v", response.Errors)
	var object map[string]any
	require.NoError(t, json.Unmarshal(response.Data[root], &object))
	value, ok := object[field].(string)
	require.True(t, ok, "field %s.%s missing in %s", root, field, string(response.Data[root]))
	return value
}

// errorMessage returns the first GraphQL error message, failing when there is none.
func errorMessage(t *testing.T, response graphQLResponse) string {
	t.Helper()
	require.NotEmpty(t, response.Errors, "expected a GraphQL error, got data %v", response.Data)
	return response.Errors[0].Message
}

const (
	ssoLoginMutation = `mutation($account: String!, $password: String!, $totp: String) {
		UserLogin(account: $account, password: $password, totp_code: $totp) { token }
	}`
	ssoWhoAmIQuery           = `query { WhoAmI { id username } }`
	ssoProfileQuery          = `query { UserProfile { uid account } }`
	ssoPasskeyStartMutation  = `mutation { UserStartPasskeyLogin { options_json session } }`
	ssoPasskeyFinishMutation = `mutation($session: String!, $credential: String!) {
		UserFinishPasskeyLogin(session: $session, credential_json: $credential) { token redirect_to }
	}`
)

// login runs password login and returns the GraphQL response.
func (e *ssoE2E) login(t *testing.T, account string, password string, totp *string) graphQLResponse {
	t.Helper()
	variables := map[string]any{"account": account, "password": password}
	if totp != nil {
		variables["totp"] = *totp
	}
	return e.graphql(t, "", ssoLoginMutation, variables)
}

// whoAmI resolves the SSO UID behind a bearer token.
func (e *ssoE2E) whoAmI(t *testing.T, token string) graphQLResponse {
	t.Helper()
	return e.graphql(t, token, ssoWhoAmIQuery, nil)
}

// legacyMongoUser describes a stored blog account fixture.
type legacyMongoUser struct {
	ID       primitive.ObjectID
	UID      string
	Account  string
	Password string
}

// insertLegacyUser stores a blog account the way pre-SSO documents look: no
// status field unless extra provides one, and a gcrypto SHA-256 password hash.
func (e *ssoE2E) insertLegacyUser(t *testing.T, account string, password string, withUID bool, extra bson.M) legacyMongoUser {
	t.Helper()
	hash, err := gcrypto.PasswordHash([]byte(password), gutils.HashTypeSha256)
	require.NoError(t, err)
	user := legacyMongoUser{ID: primitive.NewObjectID(), Account: account, Password: password}
	doc := bson.M{
		"_id":               user.ID,
		"account":           account,
		"username":          "Legacy Owner",
		"password":          hash,
		"post_modified_gmt": time.Now().UTC(),
	}
	if withUID {
		user.UID = gutils.UUID7()
		doc["uid"] = user.UID
	}
	for key, value := range extra {
		doc[key] = value
	}
	_, err = e.db.Collection("users").InsertOne(context.Background(), doc)
	require.NoError(t, err)
	return user
}

// virtualPasskey is a software ES256 authenticator producing real WebAuthn
// assertions, so the server's full verification path runs unmodified.
type virtualPasskey struct {
	credentialID []byte
	key          *ecdsa.PrivateKey
	counter      uint32
}

// newVirtualPasskey creates a fresh discoverable credential.
func newVirtualPasskey(t *testing.T) *virtualPasskey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	id := make([]byte, 32)
	_, err = rand.Read(id)
	require.NoError(t, err)
	return &virtualPasskey{credentialID: id, key: key}
}

// storedPasskey returns the blog passkey record holding this credential.
func (p *virtualPasskey) storedPasskey(t *testing.T) blogModel.PasskeyCredential {
	t.Helper()
	ecdhKey, err := p.key.PublicKey.ECDH()
	require.NoError(t, err)
	point := ecdhKey.Bytes() // 0x04 || X || Y
	cose, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{
			KeyType:   int64(webauthncose.EllipticKey),
			Algorithm: int64(webauthncose.AlgES256),
		},
		Curve:  int64(webauthncose.P256),
		XCoord: point[1:33],
		YCoord: point[33:],
	})
	require.NoError(t, err)
	credentialJSON, err := json.Marshal(webauthn.Credential{
		ID:              p.credentialID,
		PublicKey:       cose,
		AttestationType: "none",
		Authenticator:   webauthn.Authenticator{SignCount: p.counter},
	})
	require.NoError(t, err)
	return blogModel.PasskeyCredential{
		ID:             base64.RawURLEncoding.EncodeToString(p.credentialID),
		Name:           "virtual",
		PublicKey:      base64.RawURLEncoding.EncodeToString(cose),
		CredentialJSON: string(credentialJSON),
		SignCount:      p.counter,
		CreatedAt:      time.Now().UTC(),
	}
}

// assertion signs a WebAuthn get() response for challenge as the browser would.
// signCount lets a test emit a stale counter; pass p.counter+1 for a normal login.
func (p *virtualPasskey) assertion(t *testing.T, challenge string, origin string, userHandle []byte, signCount uint32) string {
	t.Helper()
	clientData, err := json.Marshal(map[string]any{
		"type":        "webauthn.get",
		"challenge":   challenge,
		"origin":      origin,
		"crossOrigin": false,
	})
	require.NoError(t, err)
	rpIDHash := sha256.Sum256([]byte(ssoE2ERPID))
	authData := make([]byte, 0, 37)
	authData = append(authData, rpIDHash[:]...)
	authData = append(authData, 0x01|0x04) // user present + user verified
	authData = binary.BigEndian.AppendUint32(authData, signCount)
	clientHash := sha256.Sum256(clientData)
	digest := sha256.Sum256(append(append([]byte{}, authData...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, p.key, digest[:])
	require.NoError(t, err)

	encode := base64.RawURLEncoding.EncodeToString
	response := map[string]any{
		"clientDataJSON":    encode(clientData),
		"authenticatorData": encode(authData),
		"signature":         encode(signature),
	}
	if len(userHandle) > 0 {
		response["userHandle"] = encode(userHandle)
	}
	credential, err := json.Marshal(map[string]any{
		"id":                     encode(p.credentialID),
		"rawId":                  encode(p.credentialID),
		"type":                   "public-key",
		"response":               response,
		"clientExtensionResults": map[string]any{},
	})
	require.NoError(t, err)
	return string(credential)
}

// startPasskeyLogin begins a discoverable ceremony and returns the challenge and signed session.
func (e *ssoE2E) startPasskeyLogin(t *testing.T) (challenge string, session string) {
	t.Helper()
	response := e.graphql(t, "", ssoPasskeyStartMutation, nil)
	optionsJSON := stringField(t, response, "UserStartPasskeyLogin", "options_json")
	session = stringField(t, response, "UserStartPasskeyLogin", "session")
	var options struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	require.NoError(t, json.Unmarshal([]byte(optionsJSON), &options))
	require.NotEmpty(t, options.PublicKey.Challenge)
	return options.PublicKey.Challenge, session
}

// finishPasskeyLogin submits a signed assertion for a signed session.
func (e *ssoE2E) finishPasskeyLogin(t *testing.T, session string, credential string) graphQLResponse {
	t.Helper()
	return e.graphql(t, "", ssoPasskeyFinishMutation, map[string]any{"session": session, "credential": credential})
}

// addPasskey attaches a credential record to a stored user.
func (e *ssoE2E) addPasskey(t *testing.T, userID primitive.ObjectID, passkey blogModel.PasskeyCredential) {
	t.Helper()
	result, err := e.db.Collection("users").UpdateOne(context.Background(),
		bson.M{"_id": userID}, bson.M{"$push": bson.M{"passkeys": passkey}})
	require.NoError(t, err)
	require.EqualValues(t, 1, result.MatchedCount)
}
