package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

const serviceAlertSyntheticToken = "SYNTHETIC_TRANSPORT_TOKEN_NEVER_IN_ERRORS"

// serviceAlertWire records only synthetic requests to the loopback Bot API fixture.
type serviceAlertWire struct {
	method      string
	path        string
	contentType string
	fields      map[string]string
	filename    string
	payload     []byte
	err         error
}

// serviceAlertReadWire decodes JSON or multipart directly in memory without upload files.
func serviceAlertReadWire(r *http.Request) serviceAlertWire {
	wire := serviceAlertWire{method: r.Method, path: r.URL.Path, contentType: r.Header.Get("Content-Type"), fields: make(map[string]string)}
	mediaType, params, err := mime.ParseMediaType(wire.contentType)
	if err != nil {
		wire.err = err
		return wire
	}
	if mediaType == "application/json" {
		var raw map[string]json.RawMessage
		wire.err = json.NewDecoder(r.Body).Decode(&raw)
		for key, value := range raw {
			var text string
			if err := json.Unmarshal(value, &text); err == nil {
				wire.fields[key] = text
			} else {
				wire.fields[key] = string(value)
			}
		}
		wire.payload = []byte(wire.fields["text"])
		return wire
	}
	if mediaType != "multipart/form-data" {
		wire.err = errors.New("unexpected fixture media type")
		return wire
	}
	reader := multipart.NewReader(r.Body, params["boundary"])
	for {
		part, readErr := reader.NextPart()
		if errors.Is(readErr, io.EOF) {
			return wire
		}
		if readErr != nil {
			wire.err = readErr
			return wire
		}
		value, readErr := io.ReadAll(part)
		closeErr := part.Close()
		if readErr != nil {
			wire.err = readErr
			return wire
		}
		if closeErr != nil {
			wire.err = closeErr
			return wire
		}
		if part.FormName() == "document" {
			wire.filename = part.FileName()
			wire.payload = value
		} else {
			wire.fields[part.FormName()] = string(value)
		}
	}
}

// serviceAlertReplySuccess emits the actual API envelope and echoes the submitted representation.
func serviceAlertReplySuccess(w http.ResponseWriter, wire serviceAlertWire) {
	result := map[string]any{"message_id": 17}
	if strings.HasSuffix(wire.path, "/sendDocument") {
		result["document"] = map[string]any{"file_id": "synthetic-file-id", "file_size": len(wire.payload)}
	} else {
		result["text"] = string(wire.payload)
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result}); err != nil {
		return
	}
}

// serviceAlertFixture creates an isolated client whose only destination is a loopback server.
func serviceAlertFixture(t *testing.T, reply func(http.ResponseWriter, *http.Request, serviceAlertWire)) (*Telegram, <-chan serviceAlertWire, *atomic.Int32) {
	t.Helper()
	previousThreshold := gconfig.Shared.GetInt("settings.telegram.max_len")
	gconfig.Shared.Set("settings.telegram.max_len", 4096)
	t.Cleanup(func() { gconfig.Shared.Set("settings.telegram.max_len", previousThreshold) })
	requests := make(chan serviceAlertWire, 16)
	calls := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		wire := serviceAlertReadWire(r)
		requests <- wire
		if reply != nil {
			reply(w, r, wire)
		} else {
			serviceAlertReplySuccess(w, wire)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(server.CloseClientConnections)
	return &Telegram{bot: &tb.Bot{Token: serviceAlertSyntheticToken, URL: server.URL}, alertClient: server.Client()}, requests, calls
}

// serviceAlertNextWire consumes a completed synthetic request with a bounded test deadline.
func serviceAlertNextWire(t *testing.T, requests <-chan serviceAlertWire) serviceAlertWire {
	t.Helper()
	select {
	case wire := <-requests:
		require.NoError(t, wire.err)
		require.Equal(t, http.MethodPost, wire.method)
		require.Equal(t, "12345", wire.fields["chat_id"])
		require.NotContains(t, wire.fields, "parse_mode")
		require.NotContains(t, wire.fields, "entities")
		return wire
	case <-time.After(3 * time.Second):
		t.Fatal("synthetic Bot API request was not observed")
		return serviceAlertWire{}
	}
}

// serviceAlertSafeError verifies that transport authentication and remote text never escape errors.
func serviceAlertSafeError(t *testing.T, err error) *AlertDeliveryError {
	t.Helper()
	require.Error(t, err)
	for _, rendered := range []string{err.Error(), fmt.Sprintf("%+v", err)} {
		require.NotContains(t, rendered, serviceAlertSyntheticToken)
		require.NotContains(t, rendered, "SYNTHETIC_REMOTE_DESCRIPTION")
		require.NotContains(t, rendered, "SYNTHETIC_ORIGINAL_CONTENT")
	}
	var deliveryErr *AlertDeliveryError
	require.ErrorAs(t, err, &deliveryErr)
	return deliveryErr
}

// TestAlertDeliveryLiteralShortText preserves application-chosen content with no formatting mode.
func TestAlertDeliveryLiteralShortText(t *testing.T) {
	service, requests, calls := serviceAlertFixture(t, nil)
	msg := "type >>>>>>>>>>>>>>>>>> \nSYNTHETIC_ORIGINAL_CONTENT\n<hello> & **markdown** [literal](https://example.test/?token=chosen)\n\t你好 😀\x00"
	require.NoError(t, service.SendAlertToUser(context.Background(), 12345, msg))
	wire := serviceAlertNextWire(t, requests)
	require.Equal(t, "/bot"+serviceAlertSyntheticToken+"/sendMessage", wire.path)
	require.Equal(t, msg, string(wire.payload))
	require.Equal(t, int32(1), calls.Load())
}

// TestAlertDeliveryUTF16Boundary counts supplementary characters without splitting or clipping.
func TestAlertDeliveryUTF16Boundary(t *testing.T) {
	for _, tc := range []struct{ name, msg, method string }{
		{"BMP at boundary", strings.Repeat("界", 4096), "sendMessage"},
		{"BMP above boundary", strings.Repeat("界", 4097), "sendDocument"},
		{"supplementary at boundary", strings.Repeat("😀", 2048), "sendMessage"},
		{"supplementary above boundary", strings.Repeat("😀", 2049), "sendDocument"},
		{"mixed at boundary", strings.Repeat("😀", 2047) + "ab", "sendMessage"},
		{"mixed above boundary", strings.Repeat("😀", 2047) + "abc", "sendDocument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, requests, calls := serviceAlertFixture(t, nil)
			require.NoError(t, service.SendAlertToUser(context.Background(), 12345, tc.msg))
			wire := serviceAlertNextWire(t, requests)
			require.Equal(t, "/bot"+serviceAlertSyntheticToken+"/"+tc.method, wire.path)
			require.Equal(t, []byte(tc.msg), wire.payload)
			if tc.method == "sendDocument" {
				require.Equal(t, "alert.txt", wire.filename)
				require.NotContains(t, wire.fields, "caption")
			}
			require.Equal(t, int32(1), calls.Load())
		})
	}
}

// TestAlertDeliveryDocumentWholePayload includes exact limit bytes and rejects larger inputs intact.
func TestAlertDeliveryDocumentWholePayload(t *testing.T) {
	service, requests, calls := serviceAlertFixture(t, nil)
	require.Equal(t, 2*1024*1024, MaxAlertPayloadBytes)
	msg := strings.Repeat("x", MaxAlertPayloadBytes-len("\n原文尾部😀")) + "\n原文尾部😀"
	require.NoError(t, service.SendAlertToUser(context.Background(), 12345, msg))
	wire := serviceAlertNextWire(t, requests)
	require.Equal(t, "/bot"+serviceAlertSyntheticToken+"/sendDocument", wire.path)
	require.Equal(t, "alert.txt", wire.filename)
	require.NotContains(t, wire.fields, "caption")
	require.Equal(t, []byte(msg), wire.payload)
	require.Error(t, service.SendAlertToUser(context.Background(), 12345, msg+"x"))
	require.Equal(t, int32(1), calls.Load(), "over-limit entry must not trigger any Telegram request")
}

// TestAlertDeliveryInvalidTextNeverSends rejects invalid UTF-8 rather than JSON replacement.
func TestAlertDeliveryInvalidTextNeverSends(t *testing.T) {
	service, _, calls := serviceAlertFixture(t, nil)
	for _, msg := range []string{"", string([]byte{0xff}), "valid-prefix" + string([]byte{0xc3, 0x28})} {
		require.Error(t, service.SendAlertToUser(context.Background(), 12345, msg))
	}
	require.Equal(t, int32(0), calls.Load())
}

// TestAlertDeliveryAPIErrorsAreVisibleAndNeverRetried preserves numeric rejection metadata safely.
func TestAlertDeliveryAPIErrorsAreVisibleAndNeverRetried(t *testing.T) {
	for _, tc := range []struct {
		name                string
		status, code, retry int
	}{
		{"bad request", 400, 400, 0}, {"flood", 429, 429, 23}, {"HTTP200 API rejection", 200, 403, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, requests, calls := serviceAlertFixture(t, func(w http.ResponseWriter, _ *http.Request, _ serviceAlertWire) {
				w.WriteHeader(tc.status)
				if err := json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": tc.code, "description": "SYNTHETIC_REMOTE_DESCRIPTION", "parameters": map[string]int{"retry_after": tc.retry}}); err != nil {
					return
				}
			})
			err := service.SendAlertToUser(context.Background(), 12345, "SYNTHETIC_ORIGINAL_CONTENT")
			classified := serviceAlertSafeError(t, err)
			require.Equal(t, tc.status, classified.HTTPStatus)
			require.Equal(t, tc.code, classified.APIErrorCode)
			require.Equal(t, tc.retry, classified.RetryAfterSeconds)
			require.False(t, classified.Unconfirmed, "an explicit API rejection is a known non-acceptance")
			serviceAlertNextWire(t, requests)
			require.Equal(t, int32(1), calls.Load(), "even retry_after must not silently resend")
		})
	}
}

// TestAlertDeliveryMalformedSuccessIsUnconfirmed never mistakes absent or altered output for delivery.
func TestAlertDeliveryMalformedSuccessIsUnconfirmed(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"invalid JSON", "not-json SYNTHETIC_REMOTE_DESCRIPTION"},
		{"missing ok", `{"result":{"message_id":17,"text":"SYNTHETIC_ORIGINAL_CONTENT"}}`},
		{"missing result", `{"ok":true}`},
		{"missing ID", `{"ok":true,"result":{"text":"SYNTHETIC_ORIGINAL_CONTENT"}}`},
		{"altered text", `{"ok":true,"result":{"message_id":17,"text":"altered"}}`},
		{"missing text", `{"ok":true,"result":{"message_id":17}}`},
		{"trailing JSON", `{"ok":true,"result":{"message_id":17,"text":"SYNTHETIC_ORIGINAL_CONTENT"}} {}`},
		{"oversized response", strings.Repeat(" ", 3*1024*1024) + `{"ok":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, requests, calls := serviceAlertFixture(t, func(w http.ResponseWriter, _ *http.Request, _ serviceAlertWire) {
				if _, err := io.WriteString(w, tc.body); err != nil {
					return
				}
			})
			classified := serviceAlertSafeError(t, service.SendAlertToUser(context.Background(), 12345, "SYNTHETIC_ORIGINAL_CONTENT"))
			require.True(t, classified.Unconfirmed)
			serviceAlertNextWire(t, requests)
			require.Equal(t, int32(1), calls.Load())
		})
	}
}

// TestAlertDeliveryDocumentAcknowledgement requires a valid file reference and matching reported bytes.
func TestAlertDeliveryDocumentAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		success    bool
	}{
		{"missing file ID", `{"ok":true,"result":{"message_id":17,"document":{"file_size":5000}}}`, false},
		{"wrong file size", `{"ok":true,"result":{"message_id":17,"document":{"file_id":"synthetic","file_size":4999}}}`, false},
		{"omitted optional size", `{"ok":true,"result":{"message_id":17,"document":{"file_id":"synthetic"}}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, requests, calls := serviceAlertFixture(t, func(w http.ResponseWriter, _ *http.Request, _ serviceAlertWire) {
				if _, err := io.WriteString(w, tc.body); err != nil {
					return
				}
			})
			err := service.SendAlertToUser(context.Background(), 12345, strings.Repeat("x", 5000))
			if tc.success {
				require.NoError(t, err)
			} else {
				require.True(t, serviceAlertSafeError(t, err).Unconfirmed)
			}
			require.Len(t, serviceAlertNextWire(t, requests).payload, 5000)
			require.Equal(t, int32(1), calls.Load())
		})
	}
}

// TestAlertDeliveryExplicitSecondCallGetsFreshDocument resends only on a new caller decision.
func TestAlertDeliveryExplicitSecondCallGetsFreshDocument(t *testing.T) {
	var replies atomic.Int32
	service, requests, calls := serviceAlertFixture(t, func(w http.ResponseWriter, _ *http.Request, wire serviceAlertWire) {
		if replies.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			if _, err := io.WriteString(w, `{"ok":false,"error_code":400}`); err != nil {
				return
			}
			return
		}
		serviceAlertReplySuccess(w, wire)
	})
	msg := strings.Repeat("完整内容 😀\n", 700)
	require.Error(t, service.SendAlertToUser(context.Background(), 12345, msg))
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, []byte(msg), serviceAlertNextWire(t, requests).payload)
	require.NoError(t, service.SendAlertToUser(context.Background(), 12345, msg))
	require.Equal(t, []byte(msg), serviceAlertNextWire(t, requests).payload)
	require.Equal(t, int32(2), calls.Load())
}

// TestAlertDeliveryCancellationStopsActualHTTP observes cancellation after both request forms arrive.
func TestAlertDeliveryCancellationStopsActualHTTP(t *testing.T) {
	for _, msg := range []string{"SYNTHETIC_ORIGINAL_CONTENT", strings.Repeat("x", 5000)} {
		t.Run(fmt.Sprint(len(msg)), func(t *testing.T) {
			handlerCanceled := make(chan struct{}, 1)
			service, requests, calls := serviceAlertFixture(t, func(w http.ResponseWriter, r *http.Request, wire serviceAlertWire) {
				select {
				case <-r.Context().Done():
					handlerCanceled <- struct{}{}
				case <-time.After(time.Second):
					serviceAlertReplySuccess(w, wire)
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- service.SendAlertToUser(ctx, 12345, msg) }()
			serviceAlertNextWire(t, requests)
			cancel()
			select {
			case err := <-result:
				require.ErrorIs(t, err, context.Canceled)
				require.True(t, serviceAlertSafeError(t, err).Unconfirmed)
			case <-time.After(3 * time.Second):
				t.Fatal("delivery ignored cancellation")
			}
			select {
			case <-handlerCanceled:
			case <-time.After(3 * time.Second):
				t.Fatal("actual HTTP request was not canceled")
			}
			require.Equal(t, int32(1), calls.Load())
		})
	}
}

// TestAlertDeliveryPreCanceledContextNeverSends rejects admission before any transport activity.
func TestAlertDeliveryPreCanceledContextNeverSends(t *testing.T) {
	service, _, calls := serviceAlertFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, service.SendAlertToUser(ctx, 12345, "chosen"), context.Canceled)
	require.Equal(t, int32(0), calls.Load())
}

// TestAlertDeliveryDroppedResponseNeverRetries reports an ambiguous single completed send.
func TestAlertDeliveryDroppedResponseNeverRetries(t *testing.T) {
	service, requests, calls := serviceAlertFixture(t, func(w http.ResponseWriter, _ *http.Request, _ serviceAlertWire) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		if err = conn.Close(); err != nil {
			return
		}
	})
	classified := serviceAlertSafeError(t, service.SendAlertToUser(context.Background(), 12345, "SYNTHETIC_ORIGINAL_CONTENT"))
	require.True(t, classified.Unconfirmed)
	serviceAlertNextWire(t, requests)
	require.Equal(t, int32(1), calls.Load())
}

// TestAlertDeliveryRedirectNeverForwardsTokenOrContent blocks cross-destination redirects.
func TestAlertDeliveryRedirectNeverForwardsTokenOrContent(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationCalls.Add(1)
		if _, err := io.WriteString(w, `{"ok":true,"result":{"message_id":17,"text":"SYNTHETIC_ORIGINAL_CONTENT"}}`); err != nil {
			return
		}
	}))
	defer destination.Close()
	service, requests, calls := serviceAlertFixture(t, func(w http.ResponseWriter, r *http.Request, _ serviceAlertWire) {
		http.Redirect(w, r, destination.URL+"/bot"+serviceAlertSyntheticToken+"/sendMessage", http.StatusTemporaryRedirect)
	})
	require.Error(t, service.SendAlertToUser(context.Background(), 12345, "SYNTHETIC_ORIGINAL_CONTENT"))
	serviceAlertNextWire(t, requests)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, int32(0), destinationCalls.Load())
	require.Nil(t, service.alertClient.CheckRedirect, "shared client must not be mutated")
}

// TestAlertDeliveryConfiguredThreshold changes routing without changing original content.
func TestAlertDeliveryConfiguredThreshold(t *testing.T) {
	for _, tc := range []struct {
		name             string
		threshold, units int
		method           string
	}{
		{"configured short at", 20, 20, "sendMessage"},
		{"configured short above", 20, 21, "sendDocument"},
		{"default at", 0, 3000, "sendMessage"},
		{"default above", 0, 3001, "sendDocument"},
		{"invalid above platform", 4097, 3001, "sendDocument"},
		{"invalid negative", -1, 3001, "sendDocument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, requests, calls := serviceAlertFixture(t, nil)
			gconfig.Shared.Set("settings.telegram.max_len", tc.threshold)
			msg := strings.Repeat("x", tc.units)
			require.NoError(t, service.SendAlertToUser(context.Background(), 12345, msg))
			wire := serviceAlertNextWire(t, requests)
			require.Equal(t, "/bot"+serviceAlertSyntheticToken+"/"+tc.method, wire.path)
			require.Equal(t, []byte(msg), wire.payload)
			require.Equal(t, int32(1), calls.Load())
		})
	}
}

// TestAlertDeliveryDeadlineCancelsActualHTTP retains the context cause without resending.
func TestAlertDeliveryDeadlineCancelsActualHTTP(t *testing.T) {
	service, requests, calls := serviceAlertFixture(t, func(w http.ResponseWriter, r *http.Request, wire serviceAlertWire) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
			serviceAlertReplySuccess(w, wire)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := service.SendAlertToUser(ctx, 12345, "SYNTHETIC_ORIGINAL_CONTENT")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, serviceAlertSafeError(t, err).Unconfirmed)
	serviceAlertNextWire(t, requests)
	require.Equal(t, int32(1), calls.Load())
}

// TestAlertDeliveryHTTPFailureCannotClaimSuccess rejects even a valid-looking success body on HTTP failure.
func TestAlertDeliveryHTTPFailureCannotClaimSuccess(t *testing.T) {
	service, requests, calls := serviceAlertFixture(t, func(w http.ResponseWriter, _ *http.Request, wire serviceAlertWire) {
		w.WriteHeader(http.StatusInternalServerError)
		serviceAlertReplySuccess(w, wire)
	})
	err := service.SendAlertToUser(context.Background(), 12345, "SYNTHETIC_ORIGINAL_CONTENT")
	require.Equal(t, http.StatusInternalServerError, serviceAlertSafeError(t, err).HTTPStatus)
	serviceAlertNextWire(t, requests)
	require.Equal(t, int32(1), calls.Load())
}
