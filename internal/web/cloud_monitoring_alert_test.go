package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	telegram "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/controller"
	telegramDao "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dao"
	telegramSvc "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
)

// TestCloudMonitoringRouteFollowsPrefixes proves the webhook is reachable at each
// mounted path and challenges before it needs any Telegram dependency.
func TestCloudMonitoringRouteFollowsPrefixes(t *testing.T) {
	setupGinTestMode()
	for _, prefix := range []urlPrefixConfig{{}, {internal: "/mcp", public: "/mcp"}, {internal: "/mcp", public: ""}} {
		router := gin.New()
		registerCloudMonitoringAlertRoutes(router, prefix, &Resolver{args: ResolverArgs{TelegramSvc: new(telegramSvc.Telegram)}})
		for _, path := range ssoRoutePaths(prefix, cloudMonitoringAlertPath) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
			require.Equal(t, http.StatusUnauthorized, response.Code, path)
			require.Contains(t, response.Header().Get("WWW-Authenticate"), "Basic")

			response = httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			require.Equal(t, http.StatusNotFound, response.Code, path)
		}
	}
}

// newAlertMongo returns an ephemeral database, or skips without the contract URI.
func newAlertMongo(t *testing.T, ctx context.Context) *mongo.Database {
	t.Helper()
	uri := os.Getenv("MONGO_QUERY_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_QUERY_TEST_URI is required; the Mongo contract CI job always sets it")
	}
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	require.NoError(t, err)
	db := client.Database("telegram_alert_" + primitive.NewObjectID().Hex())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		require.NoError(t, db.Drop(cleanupCtx))
		require.NoError(t, client.Disconnect(cleanupCtx))
	})
	return db
}

// TestMongoAlertCredentialClasses runs the real DAO against MongoDB, because the
// webhook's 401 versus 503 answer depends on this classification and fakes cannot
// prove it. The refused paths also keep their GraphQL error text.
func TestMongoAlertCredentialClasses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db := newAlertMongo(t, ctx)
	_, err := db.Collection("alert_types").InsertOne(ctx, bson.M{"name": "synthetic_alert", "push_token": "synthetic-token"})
	require.NoError(t, err)
	monitor := telegramDao.NewMonitor(ssoMongoDB{db})

	alert, err := monitor.ValidateTokenForAlertType(ctx, "synthetic-token", "synthetic_alert")
	require.NoError(t, err)
	require.Equal(t, "synthetic_alert", alert.Name)

	for _, tc := range []struct {
		name, token, alertType, message string
	}{
		{name: "wrong token", token: "wrong", alertType: "synthetic_alert", message: "token invalidate for `synthetic_alert`"},
		{name: "unknown type", token: "synthetic-token", alertType: "unknown", message: "alert_type `unknown` not found"},
		{name: "blank token", token: " ", alertType: "synthetic_alert", message: "push token is required"},
		{name: "blank type", token: "synthetic-token", alertType: " ", message: "alert name is required"},
		{name: "oversized token", token: strings.Repeat("x", 65), alertType: "synthetic_alert", message: "push token exceeds max length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := monitor.ValidateTokenForAlertType(ctx, tc.token, tc.alertType)
			require.ErrorIs(t, err, telegramSvc.ErrAlertUnauthorized)
			require.ErrorContains(t, err, tc.message)
		})
	}

	canceled, cancelNow := context.WithCancel(ctx)
	cancelNow()
	_, err = monitor.ValidateTokenForAlertType(canceled, "synthetic-token", "synthetic_alert")
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, errors.Is(err, telegramSvc.ErrAlertUnauthorized), "a storage failure must not read as refused credentials")
}

// TestMongoCloudMonitoringWebhookEndToEnd drives the whole receiver over real
// HTTP: gin route, DAO on MongoDB and the actual Telegram sender. Only Telegram's
// own server is replaced, by the loopback bot that records its wire payloads.
func TestMongoCloudMonitoringWebhookEndToEnd(t *testing.T) {
	setupGinTestMode()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db := newAlertMongo(t, ctx)
	for _, key := range []string{"tasks", "settings.telegram.max_len", "settings.telegram.throttle.total_burst", "settings.telegram.throttle.total_per_sec",
		"settings.telegram.throttle.each_title_per_sec", "settings.telegram.throttle.each_title_burst"} {
		prior := gconfig.Shared.Get(key)
		t.Cleanup(func() { gconfig.Shared.Set(key, prior) })
		gconfig.Shared.Set(key, 1000)
	}
	gconfig.Shared.Set("tasks", []string{})
	gconfig.Shared.Set("settings.telegram.max_len", 3000)

	// Two subscribers, one with a duplicate subscription row.
	alertID := primitive.NewObjectID()
	_, err := db.Collection("alert_types").InsertOne(ctx, bson.M{"_id": alertID, "name": receiverAlertType, "push_token": receiverAlertToken})
	require.NoError(t, err)
	first, second := primitive.NewObjectID(), primitive.NewObjectID()
	_, err = db.Collection("users").InsertMany(ctx, []any{bson.M{"_id": first, "uid": 101}, bson.M{"_id": second, "uid": 102}})
	require.NoError(t, err)
	_, err = db.Collection("user_alert_relations").InsertMany(ctx, []any{
		bson.M{"alert_id": alertID, "user_id": first},
		bson.M{"alert_id": alertID, "user_id": second},
		bson.M{"alert_id": alertID, "user_id": second},
	})
	require.NoError(t, err)

	bot := &receiverBot{failures: map[string]string{}}
	endpoint := httptest.NewServer(bot)
	t.Cleanup(endpoint.Close)
	svc, err := telegramSvc.New(ctx, telegramDao.NewMonitor(ssoMongoDB{db}), nil, nil, nil, receiverAlertToken, endpoint.URL)
	require.NoError(t, err)
	telegram.NewTelegram(ctx, svc)
	router := gin.New()
	registerCloudMonitoringAlertRoutes(router, urlPrefixConfig{}, &Resolver{args: ResolverArgs{TelegramSvc: svc}})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	post := func(username, password string) *http.Response {
		const body = `{"version":"1.2","incident":{"incident_id":"0.e2e","state":"open","severity":"Critical","policy_name":"CVM Uptime Failure",
			"resource_display_name":"gateway","summary":"Check failed.","started_at":1762000000,"ended_at":null}}`
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+cloudMonitoringAlertPath+"?env=prod", strings.NewReader(body))
		require.NoError(t, err)
		if username != "" {
			request.SetBasicAuth(username, password)
		}
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		return response
	}

	response := post("", "")
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	require.Contains(t, response.Header.Get("WWW-Authenticate"), "Basic")
	require.Equal(t, http.StatusUnauthorized, post(receiverAlertType, "wrong").StatusCode)
	require.Equal(t, http.StatusUnauthorized, post("unknown", receiverAlertToken).StatusCode)
	deliveries, parseErrors := bot.snapshot()
	require.Empty(t, deliveries, "a refused request must not reach Telegram")
	require.Empty(t, parseErrors)

	require.Equal(t, http.StatusNoContent, post(receiverAlertType, receiverAlertToken).StatusCode)
	deliveries, parseErrors = bot.snapshot()
	require.Empty(t, parseErrors)
	require.Len(t, deliveries, 2, "each subscriber is sent one message despite the duplicate row")
	recipients := make([]string, 0, len(deliveries))
	for _, delivery := range deliveries {
		recipients = append(recipients, delivery.UID)
		require.Equal(t, "sendMessage", delivery.Method)
		require.Equal(t, receiverAlertType+" >>>>>>>>>>>>>>>>>> \n"+
			"[PROD] OPEN CRITICAL: CVM Uptime Failure\nresource: gateway\nCheck failed.\nstarted: 2025-11-01T12:26:40Z", delivery.Text)
		require.Empty(t, delivery.Fields["parse_mode"], "alerts stay literal text")
	}
	require.ElementsMatch(t, []string{"101", "102"}, recipients)

	bot.mu.Lock()
	bot.failures["102"] = "api"
	bot.mu.Unlock()
	require.Equal(t, http.StatusServiceUnavailable, post(receiverAlertType, receiverAlertToken).StatusCode,
		"a partial fan-out must stay visible to the sender")
}

// TestCloudMonitoringRouteUsesWebhookNamespace pins the published path.
func TestCloudMonitoringRouteUsesWebhookNamespace(t *testing.T) {
	setupGinTestMode()
	router := gin.New()
	registerCloudMonitoringAlertRoutes(router, urlPrefixConfig{}, &Resolver{args: ResolverArgs{TelegramSvc: new(telegramSvc.Telegram)}})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/webhook/gcp/alerts/cloud-monitoring", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusUnauthorized, response.Code)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/alerts/cloud-monitoring", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusNotFound, response.Code)
}

// TestCloudMonitoringDocumentedURLs pins the URL table in the README and the
// delivery manual: an unset prefix mounts only under /mcp, and the deployed
// public_url_prefix of "/" also serves the root path.
func TestCloudMonitoringDocumentedURLs(t *testing.T) {
	setupGinTestMode()
	const root, prefixed = "/webhook/gcp/alerts/cloud-monitoring", "/mcp/webhook/gcp/alerts/cloud-monitoring"
	for _, tc := range []struct {
		name             string
		internal, public any
		want             map[string]int
	}{
		{name: "prefixes unset", want: map[string]int{root: http.StatusNotFound, prefixed: http.StatusUnauthorized}},
		{name: "deployed prefixes", internal: "/mcp", public: "/", want: map[string]int{root: http.StatusUnauthorized, prefixed: http.StatusUnauthorized}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range map[string]any{"settings.web.url_prefix": tc.internal, "settings.web.public_url_prefix": tc.public} {
				prior := gconfig.Shared.Get(key)
				t.Cleanup(func() { gconfig.Shared.Set(key, prior) })
				gconfig.Shared.Set(key, value)
			}
			router := gin.New()
			registerCloudMonitoringAlertRoutes(router, newURLPrefixConfig(), &Resolver{args: ResolverArgs{TelegramSvc: new(telegramSvc.Telegram)}})
			got := make(map[string]int, len(tc.want))
			for path := range tc.want {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
				got[path] = response.Code
			}
			require.Equal(t, tc.want, got)
		})
	}
}

// TestCloudMonitoringRouteAnswersUnavailableWithoutTelegram tells the sender a
// missing backend is transient instead of the route not existing.
func TestCloudMonitoringRouteAnswersUnavailableWithoutTelegram(t *testing.T) {
	setupGinTestMode()
	for _, resolver := range []*Resolver{nil, {}} {
		router := gin.New()
		registerCloudMonitoringAlertRoutes(router, urlPrefixConfig{}, resolver)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, cloudMonitoringAlertPath, strings.NewReader(`{}`)))
		require.Equal(t, http.StatusServiceUnavailable, response.Code)
	}
}
