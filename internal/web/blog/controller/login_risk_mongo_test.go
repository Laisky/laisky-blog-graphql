package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	ginMw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v6"
	gcrypto "github.com/Laisky/go-utils/v6/crypto"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/xlzd/gotp"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/dao"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/service"
)

// switchableMongoDB serves collections from a healthy database until the test
// breaks it, after which every operation hits a client that cannot reach any
// server. It lets one test drive the same production code through a real
// database outage.
type switchableMongoDB struct {
	healthy *mongo.Database
	broken  *mongo.Database
	down    atomic.Bool
}

// current returns the database the next operation should use.
func (d *switchableMongoDB) current() *mongo.Database {
	if d.down.Load() {
		return d.broken
	}
	return d.healthy
}

// Close leaves client cleanup to the owning test.
func (d *switchableMongoDB) Close(context.Context) error { return nil }

// GetCol returns a collection from the current database.
func (d *switchableMongoDB) GetCol(name string) *mongo.Collection {
	return d.current().Collection(name)
}

// DB returns another database on the current client.
func (d *switchableMongoDB) DB(name string) *mongo.Database {
	return d.current().Client().Database(name)
}

// CurrentDB returns the current database.
func (d *switchableMongoDB) CurrentDB() *mongo.Database { return d.current() }

// loginRiskHarness wires the real controller and service to an ephemeral MongoDB.
type loginRiskHarness struct {
	db       *switchableMongoDB
	resolver *MutationResolver
	tracker  *authChallengeTracker
	now      *time.Time
}

// newLoginRiskHarness builds the harness, skipping when no MongoDB is configured.
func newLoginRiskHarness(t *testing.T) *loginRiskHarness {
	t.Helper()
	uri := os.Getenv("MONGO_QUERY_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_QUERY_TEST_URI is required; the Mongo contract CI job always sets it")
	}
	for key, value := range map[string]any{
		"settings.secret":                  "login-risk-secret-for-tests-0123456789",
		"settings.web.sso_jwt.private_key": "",
	} {
		previous := gconfig.Shared.Get(key)
		gconfig.Shared.Set(key, value)
		t.Cleanup(func() { gconfig.Shared.Set(key, previous) })
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	require.NoError(t, err)
	unreachable, err := mongo.Connect(ctx, options.Client().ApplyURI("mongodb://127.0.0.1:1/?connect=direct").
		SetServerSelectionTimeout(200*time.Millisecond).SetConnectTimeout(200*time.Millisecond))
	require.NoError(t, err)
	healthy := client.Database("login_risk_" + primitive.NewObjectID().Hex())
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		require.NoError(t, healthy.Drop(cleanup))
		require.NoError(t, client.Disconnect(cleanup))
		require.NoError(t, unreachable.Disconnect(cleanup))
	})
	db := &switchableMongoDB{healthy: healthy, broken: unreachable.Database("unreachable")}
	svc, err := service.New(ctx, glog.Shared, dao.New(glog.Shared, db, nil), nil)
	require.NoError(t, err)

	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 3, &now)
	withAuthChallengeTracker(t, tracker)
	return &loginRiskHarness{db: db, resolver: NewMutationResolver(svc), tracker: tracker, now: &now}
}

// requestContext returns a request context whose gin client IP is clientIP.
func requestContext(t *testing.T, clientIP string) context.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	gctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	gctx.Request = httptest.NewRequest(http.MethodPost, "https://sso.example.test/query", nil)
	gctx.Request.RemoteAddr = clientIP + ":4321"
	return context.WithValue(context.Background(), ginMw.CtxKeyGin, gctx)
}

// insertUser stores a legacy-shaped MongoDB account and returns its password.
func (h *loginRiskHarness) insertUser(t *testing.T, account string, extra bson.M) string {
	t.Helper()
	const password = "short-legacy-pw" // within every store limit, isolating the risk behavior under test
	hash, err := gcrypto.PasswordHash([]byte(password), gutils.HashTypeSha256)
	require.NoError(t, err)
	doc := bson.M{"_id": primitive.NewObjectID(), "uid": gutils.UUID7(), "account": account, "username": "u", "password": hash}
	for key, value := range extra {
		doc[key] = value
	}
	_, err = h.db.healthy.Collection("users").InsertOne(context.Background(), doc)
	require.NoError(t, err)
	return password
}

// failures reports how many credential failures the tracker holds for key.
func (h *loginRiskHarness) failures(key string) int {
	h.tracker.mu.Lock()
	defer h.tracker.mu.Unlock()
	client, ok := h.tracker.clients[key]
	if !ok {
		return 0
	}
	return len(keepRecent(client.failures, h.now.Add(-h.tracker.failureWindow)))
}

// TestMongoLoginInfrastructureFailureIsNotACredentialFailure proves a database
// outage neither looks like a wrong password to the user nor pushes the client
// toward a Turnstile challenge, while genuine wrong passwords still do.
func TestMongoLoginInfrastructureFailureIsNotACredentialFailure(t *testing.T) {
	h := newLoginRiskHarness(t)
	ctx := requestContext(t, "198.51.100.7")
	password := h.insertUser(t, "infra@example.test", nil)

	h.db.down.Store(true)
	for range 5 {
		_, err := h.resolver.UserLogin(ctx, "infra@example.test", password, nil, nil)
		require.Error(t, err)
		require.NotContains(t, err.Error(), model.ErrInvalidCredentials.Error(), "an outage must not be reported as bad credentials")
		require.Contains(t, err.Error(), model.ErrLoginUnavailable.Error())
	}
	require.Zero(t, h.failures("198.51.100.7"), "an outage must not count as credential failures")
	require.False(t, h.tracker.challengeRequired("198.51.100.7"))

	h.db.down.Store(false)
	for range 3 {
		_, err := h.resolver.UserLogin(ctx, "infra@example.test", password+"x", nil, nil)
		require.ErrorIs(t, err, model.ErrInvalidCredentials)
	}
	require.Equal(t, 3, h.failures("198.51.100.7"))
	require.True(t, h.tracker.challengeRequired("198.51.100.7"), "genuine failures must still trigger the risk policy")
}

// stubTurnstile answers Cloudflare siteverify locally with the given verdict and
// counts how many tokens reached it.
func stubTurnstile(t *testing.T, success bool) *atomic.Int32 {
	t.Helper()
	calls := &atomic.Int32{}
	previous := turnstileVerifyHTTPClient
	turnstileVerifyHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		body := `{"success":false,"error-codes":["invalid-input-response"]}`
		if success {
			body = `{"success":true}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	t.Cleanup(func() { turnstileVerifyHTTPClient = previous })
	setTurnstileEnabledSite(t)
	return calls
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper.
func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// TestMongoTOTPStepReusesSolvedChallenge proves that once a high-risk client has
// solved Turnstile and proven its password, the TOTP continuation does not
// demand a second challenge, and that a new credential failure revokes that
// allowance again.
func TestMongoTOTPStepReusesSolvedChallenge(t *testing.T) {
	h := newLoginRiskHarness(t)
	calls := stubTurnstile(t, true)
	const ip = "198.51.100.8"
	ctx := requestContext(t, ip)
	secret := gotp.RandomSecret(20)
	password := h.insertUser(t, "totp-risk@example.test", bson.M{"totp_enabled": true, "totp_secret": secret})
	for range 3 {
		h.tracker.recordFailure(clientSubject(ip))
	}
	require.True(t, h.tracker.challengeRequired(ip))

	_, err := h.resolver.UserLogin(ctx, "totp-risk@example.test", password, nil, nil)
	require.ErrorIs(t, err, model.ErrTurnstileRequired)

	token := "solved-turnstile-token"
	_, err = h.resolver.UserLogin(ctx, "totp-risk@example.test", password, &token, nil)
	require.ErrorIs(t, err, model.ErrTOTPRequired)
	require.EqualValues(t, 1, calls.Load())

	code := gotp.NewDefaultTOTP(secret).Now()
	resp, err := h.resolver.UserLogin(ctx, "totp-risk@example.test", password, nil, &code)
	require.NoError(t, err, "the TOTP step must not demand a second Turnstile challenge")
	require.NotEmpty(t, resp.Token)
	require.EqualValues(t, 1, calls.Load())

	// A fresh failure after the allowance must bring the challenge back.
	for range 3 {
		h.tracker.recordFailure(clientSubject(ip))
	}
	_, err = h.resolver.UserLogin(ctx, "totp-risk@example.test", password, nil, nil)
	require.ErrorIs(t, err, model.ErrTurnstileRequired)
	_, err = h.resolver.UserLogin(ctx, "totp-risk@example.test", password, &token, nil)
	require.ErrorIs(t, err, model.ErrTOTPRequired)
	bad := "000000"
	if bad == gotp.NewDefaultTOTP(secret).Now() {
		bad = "111111"
	}
	_, err = h.resolver.UserLogin(ctx, "totp-risk@example.test", password, nil, &bad)
	require.ErrorIs(t, err, model.ErrInvalidCredentials)
	_, err = h.resolver.UserLogin(ctx, "totp-risk@example.test", password, nil, &code)
	require.ErrorIs(t, err, model.ErrTurnstileRequired, "a wrong TOTP code must revoke the solved-challenge allowance")
}

// TestMongoRejectedTurnstileIsNotReportedAsBadPassword proves a Turnstile token
// that Cloudflare rejects asks the user to verify again instead of claiming the
// password was wrong.
func TestMongoRejectedTurnstileIsNotReportedAsBadPassword(t *testing.T) {
	h := newLoginRiskHarness(t)
	stubTurnstile(t, false)
	const ip = "198.51.100.9"
	ctx := requestContext(t, ip)
	password := h.insertUser(t, "turnstile-rejected@example.test", nil)
	for range 3 {
		h.tracker.recordFailure(clientSubject(ip))
	}

	token := "stale-turnstile-token"
	_, err := h.resolver.UserLogin(ctx, "turnstile-rejected@example.test", password, &token, nil)
	require.Error(t, err)
	require.NotErrorIs(t, err, model.ErrInvalidCredentials)
	require.ErrorIs(t, err, model.ErrTurnstileFailed)
}

// TestMongoAccountFailureBudgetSurvivesIPRotation proves that rotating client
// addresses cannot keep an attacked account below the challenge threshold
// (issue #84), that the account owner still signs in after one challenge from
// a fresh address, and that the success resets the account budget.
func TestMongoAccountFailureBudgetSurvivesIPRotation(t *testing.T) {
	h := newLoginRiskHarness(t)
	calls := stubTurnstile(t, true)
	password := h.insertUser(t, "victim@example.test", nil)

	for i, account := range []string{"victim@example.test", "Victim@Example.test", " VICTIM@example.test "} {
		ctx := requestContext(t, fmt.Sprintf("198.51.100.%d", 20+i))
		_, err := h.resolver.UserLogin(ctx, account, "wrong-"+password, nil, nil)
		require.ErrorIs(t, err, model.ErrInvalidCredentials, "guess %d is below the threshold", i+1)
	}

	_, err := h.resolver.UserLogin(requestContext(t, "198.51.100.30"), "victim@example.test", "wrong-again", nil, nil)
	require.ErrorIs(t, err, model.ErrTurnstileRequired, "a fresh address must not get a fresh failure budget for the same account")
	require.Zero(t, calls.Load())

	_, err = h.resolver.UserLogin(requestContext(t, "198.51.100.31"), "other@example.test", "whatever", nil, nil)
	require.ErrorIs(t, err, model.ErrInvalidCredentials, "other accounts are not challenged")

	owner := requestContext(t, "192.0.2.44")
	_, err = h.resolver.UserLogin(owner, "victim@example.test", password, nil, nil)
	require.ErrorIs(t, err, model.ErrTurnstileRequired)
	token := "solved-turnstile-token"
	resp, err := h.resolver.UserLogin(owner, "victim@example.test", password, &token, nil)
	require.NoError(t, err, "the owner signs in after one challenge")
	require.NotEmpty(t, resp.Token)
	require.EqualValues(t, 1, calls.Load())

	_, err = h.resolver.UserLogin(requestContext(t, "198.51.100.32"), "victim@example.test", "wrong-after-success", nil, nil)
	require.ErrorIs(t, err, model.ErrInvalidCredentials, "a successful sign-in resets the account budget")
}

// TestMongoAccountBudgetCountsWrongTOTPAcrossAddresses proves a correct
// password with wrong TOTP codes from rotating addresses escalates the account.
func TestMongoAccountBudgetCountsWrongTOTPAcrossAddresses(t *testing.T) {
	h := newLoginRiskHarness(t)
	stubTurnstile(t, true)
	secret := gotp.RandomSecret(20)
	password := h.insertUser(t, "totp-victim@example.test", bson.M{"totp_enabled": true, "totp_secret": secret})
	bad := "000000"
	if bad == gotp.NewDefaultTOTP(secret).Now() {
		bad = "111111"
	}

	for i := range 3 {
		_, err := h.resolver.UserLogin(requestContext(t, fmt.Sprintf("198.51.100.%d", 40+i)), "totp-victim@example.test", password, nil, &bad)
		require.ErrorIs(t, err, model.ErrInvalidCredentials)
	}
	_, err := h.resolver.UserLogin(requestContext(t, "198.51.100.50"), "totp-victim@example.test", password, nil, &bad)
	require.ErrorIs(t, err, model.ErrTurnstileRequired, "wrong TOTP codes count against the account")
}

// TestMongoEmailCodeAccountBudgetSurvivesIPRotation proves wrong email codes
// from rotating addresses escalate both the code login and new code requests
// for that account.
func TestMongoEmailCodeAccountBudgetSurvivesIPRotation(t *testing.T) {
	h := newLoginRiskHarness(t)
	stubTurnstile(t, true)
	h.insertUser(t, "code-victim@example.test", nil)

	for i := range 3 {
		_, err := h.resolver.UserLoginWithEmailCode(requestContext(t, fmt.Sprintf("198.51.100.%d", 60+i)), "code-victim@example.test", "123456", nil)
		require.ErrorIs(t, err, model.ErrInvalidCredentials)
	}

	_, err := h.resolver.UserLoginWithEmailCode(requestContext(t, "198.51.100.70"), "code-victim@example.test", "654321", nil)
	require.ErrorIs(t, err, model.ErrTurnstileRequired)
	_, err = h.resolver.UserRequestEmailCode(requestContext(t, "198.51.100.71"), "Code-Victim@example.test", "login", nil)
	require.ErrorIs(t, err, model.ErrTurnstileRequired, "requesting a new code for an attacked account also needs a challenge")
}

// TestMongoAccountBudgetIgnoresInfrastructureFailures proves a database
// outage seen from many addresses never consumes the account budget.
func TestMongoAccountBudgetIgnoresInfrastructureFailures(t *testing.T) {
	h := newLoginRiskHarness(t)
	stubTurnstile(t, true)
	password := h.insertUser(t, "outage@example.test", nil)

	h.db.down.Store(true)
	for i := range 5 {
		_, err := h.resolver.UserLogin(requestContext(t, fmt.Sprintf("198.51.100.%d", 80+i)), "outage@example.test", password+"x", nil, nil)
		require.ErrorIs(t, err, model.ErrLoginUnavailable)
	}
	h.db.down.Store(false)

	_, err := h.resolver.UserLogin(requestContext(t, "198.51.100.90"), "outage@example.test", password+"x", nil, nil)
	require.ErrorIs(t, err, model.ErrInvalidCredentials, "outages must not push the account toward a challenge")
}

// TestMongoConcurrentGuessesRespectAccountBudget reproduces review finding 2
// end to end: 50 wrong passwords against one account, sent at once from 50
// addresses, may only reach the password check up to the account threshold.
func TestMongoConcurrentGuessesRespectAccountBudget(t *testing.T) {
	h := newLoginRiskHarness(t)
	stubTurnstile(t, true)
	password := h.insertUser(t, "burst@example.test", nil)

	const n = 50
	var checked, challenged atomic.Int32
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.resolver.UserLogin(requestContext(t, fmt.Sprintf("198.51.100.%d", 100+i)), "burst@example.test", password+"-wrong", nil, nil)
			switch {
			case errors.Is(err, model.ErrInvalidCredentials):
				checked.Add(1)
			case errors.Is(err, model.ErrTurnstileRequired):
				challenged.Add(1)
			default:
				t.Errorf("unexpected login error: %v", err)
			}
		}()
	}
	wg.Wait()

	require.LessOrEqual(t, checked.Load(), int32(3), "concurrent guesses must not exceed the account threshold")
	require.EqualValues(t, n, checked.Load()+challenged.Load())
}

// TestMongoAccountBudgetFoldsSpellingVariants reproduces review finding 3
// end to end: guesses spread over case, accent and width variants of one
// account share its budget.
func TestMongoAccountBudgetFoldsSpellingVariants(t *testing.T) {
	h := newLoginRiskHarness(t)
	stubTurnstile(t, true)
	h.insertUser(t, "alice@example.test", nil)
	for i, variant := range []string{"alicé@example.test", "ａｌｉｃｅ@example.test", "àlîcë@example.test"} {
		_, err := h.resolver.UserLogin(requestContext(t, fmt.Sprintf("198.51.100.%d", 180+i)), variant, "wrong", nil, nil)
		require.ErrorIs(t, err, model.ErrInvalidCredentials)
	}
	_, err := h.resolver.UserLogin(requestContext(t, "198.51.100.190"), "alice@example.test", "wrong", nil, nil)
	require.ErrorIs(t, err, model.ErrTurnstileRequired)
}

// TestMongoWrongPasswordCarriesAccountIdentity verifies the MongoDB store
// reports which account a wrong password was tried against, without changing
// the client-visible error, and reports nothing for unknown accounts.
func TestMongoWrongPasswordCarriesAccountIdentity(t *testing.T) {
	h := newLoginRiskHarness(t)
	h.insertUser(t, "identity@example.test", nil)
	_, err := h.resolver.svc.ValidateLogin(context.Background(), "identity@example.test", "wrong")
	require.ErrorIs(t, err, model.ErrInvalidCredentials)
	require.True(t, strings.HasPrefix(model.RejectedIdentity(err), "mongo:"))
	require.NotContains(t, err.Error(), "mongo:", "the identity never reaches the error text")

	_, err = h.resolver.svc.ValidateLogin(context.Background(), "nobody@example.test", "wrong")
	require.ErrorIs(t, err, model.ErrInvalidCredentials)
	require.Empty(t, model.RejectedIdentity(err))
}

// TestMongoSigningFailureDoesNotResetRisk verifies only a delivered login response clears failures.
func TestMongoSigningFailureDoesNotResetRisk(t *testing.T) {
	h := newLoginRiskHarness(t)
	stubTurnstile(t, true)
	password := h.insertUser(t, "signing@example.test", nil)
	ctx := requestContext(t, "192.0.2.80")
	_, err := h.resolver.UserLogin(ctx, "signing@example.test", "wrong", nil, nil)
	require.ErrorIs(t, err, model.ErrInvalidCredentials)
	gconfig.Shared.Set("settings.web.sso_jwt.private_key", "invalid-test-key")
	_, err = h.resolver.UserLogin(ctx, "signing@example.test", password, nil, nil)
	require.ErrorIs(t, err, model.ErrLoginUnavailable)
	require.Equal(t, 1, h.failures("192.0.2.80"), "failed token issuance must not reset prior risk")
	gconfig.Shared.Set("settings.web.sso_jwt.private_key", "")
	resp, err := h.resolver.UserLogin(ctx, "signing@example.test", password, nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, resp.Token)
	require.Zero(t, h.failures("192.0.2.80"), "successful issuance resets risk")
}
