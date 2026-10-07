package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
		h.tracker.recordFailure(ip)
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
		h.tracker.recordFailure(ip)
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
		h.tracker.recordFailure(ip)
	}

	token := "stale-turnstile-token"
	_, err := h.resolver.UserLogin(ctx, "turnstile-rejected@example.test", password, &token, nil)
	require.Error(t, err)
	require.NotErrorIs(t, err, model.ErrInvalidCredentials)
	require.ErrorIs(t, err, model.ErrTurnstileFailed)
}
