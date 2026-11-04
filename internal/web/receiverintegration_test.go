package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"

	telegramController "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/controller"
	telegramModel "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	telegramService "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
)

const receiverAlertType = "synthetic_receiver"
const receiverAlertToken = "synthetic-receiver-token"

// receiverDelivery records the actual Telegram HTTP payload, not a service fake.
type receiverDelivery struct {
	Method, UID, Text, Filename string
	Data                        []byte
	Fields                      map[string]string
}

// receiverBot captures only loopback requests and injects transport/API failures.
type receiverBot struct {
	mu          sync.Mutex
	deliveries  []receiverDelivery
	parseErrors []string
	failures    map[string]string
}

// snapshot returns independent values after a synchronous GraphQL operation.
func (bot *receiverBot) snapshot() ([]receiverDelivery, []string) {
	bot.mu.Lock()
	defer bot.mu.Unlock()
	return append([]receiverDelivery(nil), bot.deliveries...), append([]string(nil), bot.parseErrors...)
}

// ServeHTTP supplies the pinned telebot constructor and real delivery protocol.
func (bot *receiverBot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/bot"+receiverAlertToken+"/getMe" {
		_, _ = io.WriteString(w, `{"ok":true,"result":{"id":7,"is_bot":true,"first_name":"Synthetic","username":"synthetic_bot"}}`)
		return
	}
	delivery := receiverDelivery{Method: strings.TrimPrefix(r.URL.Path, "/bot"+receiverAlertToken+"/"), Fields: map[string]string{}}
	var err error
	switch delivery.Method {
	case "sendMessage":
		var fields map[string]json.RawMessage
		err = json.NewDecoder(r.Body).Decode(&fields)
		if err == nil {
			for key, raw := range fields {
				var text string
				if json.Unmarshal(raw, &text) == nil {
					delivery.Fields[key] = text
				} else {
					delivery.Fields[key] = string(raw)
				}
			}
		}
		delivery.Text = delivery.Fields["text"]
	case "sendDocument":
		var readerErr error
		reader, readerErr := r.MultipartReader()
		err = readerErr
		if err == nil {
			for {
				part, partErr := reader.NextPart()
				if partErr == io.EOF {
					break
				}
				if partErr != nil {
					err = partErr
					break
				}
				data, readErr := io.ReadAll(part)
				if readErr != nil {
					err = readErr
					break
				}
				if part.FormName() == "document" {
					delivery.Data, delivery.Filename = data, part.FileName()
				} else {
					delivery.Fields[part.FormName()] = string(data)
				}
			}
		}
	default:
		http.Error(w, `{"ok":false}`, http.StatusNotFound)
		return
	}
	delivery.UID = delivery.Fields["chat_id"]
	bot.mu.Lock()
	if err != nil {
		bot.parseErrors = append(bot.parseErrors, err.Error())
	}
	bot.deliveries = append(bot.deliveries, delivery)
	failure := bot.failures[delivery.UID]
	bot.mu.Unlock()
	if err != nil {
		http.Error(w, `{"ok":false}`, http.StatusBadRequest)
		return
	}
	switch failure {
	case "api":
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"ok":false,"description":"synthetic-private-api-response-canary","error_code":503}`)
		return
	case "ambiguous":
		connection, _, hijackErr := w.(http.Hijacker).Hijack()
		if hijackErr == nil {
			_ = connection.Close()
		}
		return
	}
	result := map[string]any{"message_id": 23, "chat": map[string]any{"id": 101, "type": "private"}}
	if delivery.Method == "sendMessage" {
		result["text"] = delivery.Text
		if failure == "altered" {
			result["text"] = delivery.Text + " altered"
		}
	} else {
		result["document"] = map[string]any{"file_id": "synthetic-file", "file_name": delivery.Filename, "file_size": len(delivery.Data)}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

// receiverService retains the actual sender and substitutes only database reads.
type receiverService struct {
	*telegramService.Telegram
	users []*telegramModel.MonitorUsers
}

// ValidateTokenForAlertType preserves an explicit negative authentication control.
func (service *receiverService) ValidateTokenForAlertType(_ context.Context, token, alertType string) (*telegramModel.AlertTypes, error) {
	if token != receiverAlertToken || alertType != receiverAlertType {
		return nil, errors.New("synthetic invalid alert authorization")
	}
	return &telegramModel.AlertTypes{Name: alertType}, nil
}

// LoadUsersByAlertType supplies synthetic recipients without accessing MongoDB.
func (service *receiverService) LoadUsersByAlertType(context.Context, *telegramModel.AlertTypes) ([]*telegramModel.MonitorUsers, error) {
	return service.users, nil
}

// receiverHarness connects the generated GraphQL schema to a real local bot.
type receiverHarness struct {
	server *httptest.Server
	bot    *receiverBot
}

// newReceiverHarness never enables bot polling or contacts an external address.
func newReceiverHarness(t *testing.T, users []int, failures map[string]string, throttleEnabled bool) *receiverHarness {
	t.Helper()
	keys := []string{"tasks", "settings.telegram.max_len", "settings.telegram.throttle.total_burst", "settings.telegram.throttle.total_per_sec",
		"settings.telegram.throttle.each_title_per_sec", "settings.telegram.throttle.each_title_burst"}
	for _, key := range keys {
		prior := gconfig.Shared.Get(key)
		t.Cleanup(func() { gconfig.Shared.Set(key, prior) })
		if key == "tasks" {
			gconfig.Shared.Set(key, []string{})
			continue
		}
		if key == "settings.telegram.max_len" {
			gconfig.Shared.Set(key, 3000)
			continue
		}
		value := 1000
		if !throttleEnabled {
			value = 0
		}
		gconfig.Shared.Set(key, value)
	}
	bot := &receiverBot{failures: failures}
	endpoint := httptest.NewServer(bot)
	t.Cleanup(endpoint.Close)
	actual, err := telegramService.New(context.Background(), nil, nil, nil, nil, receiverAlertToken, endpoint.URL)
	require.NoError(t, err)
	fixture := &receiverService{Telegram: actual}
	for _, uid := range users {
		fixture.users = append(fixture.users, &telegramModel.MonitorUsers{UID: uid})
	}
	controller := telegramController.NewTelegram(context.Background(), actual)
	resolver := NewResolver(ResolverArgs{TelegramCtl: controller, TelegramSvc: actual})
	resolver.mutationResolver.telegramMutation.MutationResolver = telegramController.NewMutationResolver(fixture)
	graphqlHandler := handler.New(NewExecutableSchema(Config{Resolvers: resolver}))
	graphqlHandler.AddTransport(transport.POST{})
	server := httptest.NewServer(graphqlHandler)
	t.Cleanup(server.Close)
	return &receiverHarness{server: server, bot: bot}
}

// receiverRPCResponse is the actual public GraphQL response envelope.
type receiverRPCResponse struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// call sends the same public mutation/variables shape used by the SDK.
func (h *receiverHarness) call(t *testing.T, token, message string) receiverRPCResponse {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"query":     `mutation($type:String!,$token:String!,$msg:String!){TelegramMonitorAlert(type:$type,token:$token,msg:$msg){name}}`,
		"variables": map[string]string{"type": receiverAlertType, "token": token, "msg": message},
	})
	require.NoError(t, err)
	response, err := http.Post(h.server.URL, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer response.Body.Close()
	var decoded receiverRPCResponse
	require.NoError(t, json.NewDecoder(response.Body).Decode(&decoded))
	return decoded
}

// receiverUnicode produces an exact byte-sized UTF-8 fixture with markup/newlines.
func receiverUnicode(size int) string {
	const unit = "你好😀<>&_*\n"
	return strings.Repeat(unit, size/len(unit)) + strings.Repeat("x", size%len(unit))
}

// requireReceiverWire checks full payload bytes and absence of formatting/captions.
func requireReceiverWire(t *testing.T, delivery receiverDelivery, original string, document bool) {
	t.Helper()
	expected := receiverAlertType + " >>>>>>>>>>>>>>>>>> \n" + original
	require.Equal(t, "101", delivery.UID)
	require.Empty(t, delivery.Fields["parse_mode"])
	require.Empty(t, delivery.Fields["caption"])
	if document {
		require.Equal(t, "sendDocument", delivery.Method)
		require.Equal(t, "alert.txt", delivery.Filename)
		require.Equal(t, []byte(expected), delivery.Data)
	} else {
		require.Equal(t, "sendMessage", delivery.Method)
		require.Equal(t, expected, delivery.Text)
	}
}

// TestReceiverIntegrationCompleteContent crosses GraphQL and actual Telegram HTTP.
func TestReceiverIntegrationCompleteContent(t *testing.T) {
	for _, size := range []int{80, 20 * 1024, 1 << 20} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			h := newReceiverHarness(t, []int{101}, nil, true)
			message := receiverUnicode(size)
			require.Len(t, message, size, "fixture generator must preserve the requested byte budget")
			response := h.call(t, receiverAlertToken, message)
			require.Empty(t, response.Errors)
			deliveries, parseErrors := h.bot.snapshot()
			require.Empty(t, parseErrors)
			require.Len(t, deliveries, 1)
			requireReceiverWire(t, deliveries[0], message, size > 4096)
		})
	}
}

// TestReceiverIntegrationLegacyThresholdPreservesContent keeps the configured
// short-message threshold without treating it as permission to discard bytes.
func TestReceiverIntegrationLegacyThresholdPreservesContent(t *testing.T) {
	h := newReceiverHarness(t, []int{101}, nil, true)
	gconfig.Shared.Set("settings.telegram.max_len", 20)
	message := receiverUnicode(80)
	require.Empty(t, h.call(t, receiverAlertToken, message).Errors)
	deliveries, parseErrors := h.bot.snapshot()
	require.Empty(t, parseErrors)
	require.Len(t, deliveries, 1)
	requireReceiverWire(t, deliveries[0], message, true)
}

// TestReceiverIntegrationAuthorizationAndThrottle proves refusal has no bot effects.
func TestReceiverIntegrationAuthorizationAndThrottle(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		throttle    bool
	}{
		{"invalid token", "synthetic-wrong-token", true}, {"unavailable throttle", receiverAlertToken, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newReceiverHarness(t, []int{101}, nil, tc.throttle)
			response := h.call(t, tc.token, "synthetic auth control")
			require.NotEmpty(t, response.Errors)
			deliveries, parseErrors := h.bot.snapshot()
			require.Empty(t, parseErrors)
			require.Empty(t, deliveries)
		})
	}
}

// TestReceiverIntegrationDeduplicatesFanout limits one call to each destination.
func TestReceiverIntegrationDeduplicatesFanout(t *testing.T) {
	h := newReceiverHarness(t, []int{101, 101}, nil, true)
	message := receiverUnicode(20 * 1024)
	require.Empty(t, h.call(t, receiverAlertToken, message).Errors)
	deliveries, parseErrors := h.bot.snapshot()
	require.Empty(t, parseErrors)
	require.Len(t, deliveries, 1)
	requireReceiverWire(t, deliveries[0], message, true)
}

// TestReceiverIntegrationPartialRetryIsNotExactlyOnce exposes prior delivery.
func TestReceiverIntegrationPartialRetryIsNotExactlyOnce(t *testing.T) {
	h := newReceiverHarness(t, []int{101, 102}, map[string]string{"102": "api"}, true)
	message := receiverUnicode(20 * 1024)
	for attempt := 1; attempt <= 2; attempt++ {
		response := h.call(t, receiverAlertToken, message)
		require.NotEmpty(t, response.Errors)
		encoded, err := json.Marshal(response)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "synthetic-private-api-response-canary")
		require.NotContains(t, string(encoded), receiverAlertToken)
		deliveries, parseErrors := h.bot.snapshot()
		require.Empty(t, parseErrors)
		require.Len(t, deliveries, attempt*2, "no automatic retry; explicit retry repeats already delivered recipient")
		for _, delivery := range deliveries {
			require.Equal(t, []byte(receiverAlertType+" >>>>>>>>>>>>>>>>>> \n"+message), delivery.Data)
		}
	}
}

// TestReceiverIntegrationAmbiguousAndUnconfirmedDelivery never resends by itself.
func TestReceiverIntegrationAmbiguousAndUnconfirmedDelivery(t *testing.T) {
	for _, mode := range []string{"ambiguous", "altered"} {
		t.Run(mode, func(t *testing.T) {
			h := newReceiverHarness(t, []int{101}, map[string]string{"101": mode}, true)
			require.NotEmpty(t, h.call(t, receiverAlertToken, "synthetic transport control").Errors)
			deliveries, parseErrors := h.bot.snapshot()
			require.Empty(t, parseErrors)
			require.Len(t, deliveries, 1, "unconfirmed delivery must not trigger fallback/retry")
		})
	}
}

// TestReceiverIntegrationNoRecipientsIsNotDelivered prevents false acknowledgments.
func TestReceiverIntegrationNoRecipientsIsNotDelivered(t *testing.T) {
	h := newReceiverHarness(t, nil, nil, true)
	require.NotEmpty(t, h.call(t, receiverAlertToken, "synthetic no recipients").Errors)
	deliveries, _ := h.bot.snapshot()
	require.Empty(t, deliveries)
}

// TestReceiverIntegrationPinnedSDKSend covers direct queued SDK transport only.
// It deliberately does not claim fidelity of the pinned SDK's sanitizing hook.
func TestReceiverIntegrationPinnedSDKSend(t *testing.T) {
	h := newReceiverHarness(t, []int{101}, nil, true)
	alert, err := logSDK.NewAlert(context.Background(), h.server.URL, logSDK.WithAlertType(receiverAlertType), logSDK.WithAlertToken(receiverAlertToken))
	require.NoError(t, err)
	t.Cleanup(alert.Close)
	message := receiverUnicode(80)
	require.NoError(t, alert.Send(message))
	require.Eventually(t, func() bool { deliveries, _ := h.bot.snapshot(); return len(deliveries) == 1 }, 3*time.Second, 10*time.Millisecond)
	deliveries, parseErrors := h.bot.snapshot()
	require.Empty(t, parseErrors)
	requireReceiverWire(t, deliveries[0], message, false)
}

// TestReceiverIntegrationCandidateSDKHook is an explicit candidate-only campaign.
// Run with the approved exact SDK modfile; the pinned SDK is not claimed faithful.
func TestReceiverIntegrationCandidateSDKHook(t *testing.T) {
	if os.Getenv("GRAPHQL_TEST_ALERT_SDK_FIDELITY") != "1" {
		t.Skip("candidate SDK fidelity campaign is opt-in")
	}
	t.Run("direct Send 1MiB", func(t *testing.T) {
		h := newReceiverHarness(t, []int{101}, nil, true)
		alert, err := logSDK.NewAlert(context.Background(), h.server.URL,
			logSDK.WithAlertType(receiverAlertType), logSDK.WithAlertToken(receiverAlertToken),
			logSDK.WithAlertMaxMessageBytes(1<<20))
		require.NoError(t, err)
		t.Cleanup(alert.Close)
		message := receiverUnicode(1 << 20)
		require.Len(t, message, 1<<20, "fixture must contain exactly 1 MiB of UTF-8 bytes")
		require.NoError(t, alert.Send(message))
		require.Eventually(t, func() bool {
			deliveries, _ := h.bot.snapshot()
			return len(deliveries) == 1
		}, 3*time.Second, 10*time.Millisecond)
		deliveries, parseErrors := h.bot.snapshot()
		require.Empty(t, parseErrors)
		requireReceiverWire(t, deliveries[0], message, true)
	})
	for _, size := range []int{80, 20 * 1024} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			h := newReceiverHarness(t, []int{101}, nil, true)
			alert, err := logSDK.NewAlert(context.Background(), h.server.URL, logSDK.WithAlertType(receiverAlertType), logSDK.WithAlertToken(receiverAlertToken),
				logSDK.WithAlertMaxMessageBytes(1<<20))
			require.NoError(t, err)
			t.Cleanup(alert.Close)
			base := receiverUnicode(size)
			require.Len(t, base, size, "fixture generator must preserve the requested byte budget")
			original := base + "https://example.test/original?literal=synthetic-input-canary"
			hook := alert.GetZapHook()
			require.NoError(t, hook(zapcore.Entry{Level: zapcore.ErrorLevel, LoggerName: "synthetic.receiver", Message: "synthetic Unicode 你好 <>&_*", Time: time.Unix(1, 0)},
				[]zapcore.Field{zap.String("original_url", original), zap.Any("declared_output_markdown", nil)}))
			require.Eventually(t, func() bool { deliveries, _ := h.bot.snapshot(); return len(deliveries) == 1 }, 3*time.Second, 10*time.Millisecond)
			deliveries, parseErrors := h.bot.snapshot()
			require.Empty(t, parseErrors)
			payload := deliveries[0].Text
			if deliveries[0].Method == "sendDocument" {
				payload = string(deliveries[0].Data)
			}
			offset := strings.Index(payload, "\n{")
			require.GreaterOrEqual(t, offset, 0, "hook fields must be represented as ordinary JSON")
			var fields map[string]any
			require.NoError(t, json.Unmarshal([]byte(payload[offset+1:]), &fields))
			require.Equal(t, original, fields["original_url"])
			require.Contains(t, fields, "declared_output_markdown")
			require.Nil(t, fields["declared_output_markdown"])
		})
	}
}
