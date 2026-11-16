package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
)

// staticAuthenticator makes a Telegram service resolve authenticator.
func staticAuthenticator(authenticator *mcpauth.Authenticator) func() *mcpauth.Authenticator {
	return func() *mcpauth.Authenticator { return authenticator }
}

// TestValidateOneAPITokenUsesSharedAuthenticator: a key a Telegram user types
// is authenticated by the shared API key authenticator, and the binding hash
// it yields is the one existing ask_user bindings were stored under.
func TestValidateOneAPITokenUsesSharedAuthenticator(t *testing.T) {
	var calls atomic.Int64
	tg := &Telegram{keyAuthenticator: staticAuthenticator(authtest.NewIssuedOnly(&calls, "sk-issued"))}

	verified, err := tg.validateOneAPIToken(context.Background(), 42, "  sk-issued \n")
	require.NoError(t, err)
	require.True(t, verified.Verified())
	require.Equal(t, "sk-issued", verified.APIKey)
	legacy := sha256.Sum256([]byte(strings.TrimSpace("  sk-issued \n")))
	require.Equal(t, hex.EncodeToString(legacy[:]), verified.APIKeyHash, "existing ask_user bindings keep matching")

	_, err = tg.validateOneAPIToken(context.Background(), 42, "sk-unknown")
	require.ErrorIs(t, err, mcpauth.ErrCredentialRejected)
	require.False(t, mcpauth.IsUnavailable(err))
	require.Equal(t, int64(2), calls.Load())

	_, err = tg.validateOneAPIToken(context.Background(), 42, "sk-"+strings.Repeat("k", mcpauth.MaxAPIKeyLength))
	require.ErrorIs(t, err, mcpauth.ErrInvalidAuthorization)
	require.Equal(t, int64(2), calls.Load(), "an oversized key never reaches the authority")
}

// TestValidateOneAPITokenFailsClosedWithoutAuthenticator: before the
// authenticator is installed, validation is unavailable, never accepted.
func TestValidateOneAPITokenFailsClosedWithoutAuthenticator(t *testing.T) {
	tg := &Telegram{keyAuthenticator: staticAuthenticator(nil)}
	verified, err := tg.validateOneAPIToken(context.Background(), 42, "sk-anything")
	require.Nil(t, verified)
	require.True(t, mcpauth.IsUnavailable(err), "%v", err)
}

// TestTelegramKeyFloodSpendsOnlyThatUsersBudget reproduces the ungated
// Telegram validation: every message used to call one-api. Through the shared
// gate, one Telegram user's junk keys reach the authority at most
// per_source_burst times, and another user still validates.
func TestTelegramKeyFloodSpendsOnlyThatUsersBudget(t *testing.T) {
	var calls atomic.Int64
	validator := mcpauth.ValidatorFunc(func(_ context.Context, key string) error {
		calls.Add(1)
		if key == "sk-other-user" {
			return nil
		}
		return mcpauth.ErrCredentialRejected
	})
	frozen := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	authenticator, err := mcpauth.NewAuthenticator(validator, mcpauth.AuthenticatorOptions{Name: "oneapi", Now: func() time.Time { return frozen }})
	require.NoError(t, err)
	tg := &Telegram{keyAuthenticator: staticAuthenticator(authenticator)}

	for i := range 50 {
		_, err := tg.validateOneAPIToken(context.Background(), 1001, fmt.Sprintf("sk-junk-%d", i))
		require.Error(t, err)
	}
	burst := int64(mcpauth.DefaultValidationSettings().PerSourceBurst)
	require.Equal(t, burst, calls.Load(), "one Telegram user spends only its own budget")

	_, err = tg.validateOneAPIToken(context.Background(), 1001, "sk-junk-next")
	require.True(t, mcpauth.IsUnavailable(err), "the flooding user is refused locally: %v", err)
	require.ErrorIs(t, err, mcpauth.ErrUpstreamThrottled)

	verified, err := tg.validateOneAPIToken(context.Background(), 2002, "sk-other-user")
	require.NoError(t, err, "another Telegram user keeps its own budget")
	require.True(t, verified.Verified())
	require.Equal(t, burst+1, calls.Load())
}

// telegramBotRecorder serves the Bot API sendMessage calls of an offline bot
// and records their texts.
type telegramBotRecorder struct {
	mu    sync.Mutex
	texts []string
}

// newRecordingTelegram returns a Telegram service whose bot talks to a
// loopback Bot API and whose key authenticator is authenticator.
func newRecordingTelegram(t *testing.T, authenticator *mcpauth.Authenticator) (*Telegram, *telegramBotRecorder) {
	t.Helper()
	recorder := &telegramBotRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(body, &payload)
		recorder.mu.Lock()
		recorder.texts = append(recorder.texts, payload.Text)
		recorder.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"chat":{"id":42},"text":"ok"}}`)
	}))
	t.Cleanup(server.Close)
	bot, err := tb.NewBot(tb.Settings{URL: server.URL, Token: "123:synthetic", Client: server.Client(), Offline: true})
	require.NoError(t, err)
	return &Telegram{bot: bot, userStats: &sync.Map{}, keyAuthenticator: staticAuthenticator(authenticator)}, recorder
}

// last returns the most recent recorded message text.
func (r *telegramBotRecorder) last(t *testing.T) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotEmpty(t, r.texts, "the bot replied")
	return r.texts[len(r.texts)-1]
}

// TestTelegramKeyRepliesDistinguishUnavailableFromInvalid: a key the
// authority could not judge is not called invalid, and the user stays in the
// waiting state to retry; a rejected key keeps the invalid-key reply.
func TestTelegramKeyRepliesDistinguishUnavailableFromInvalid(t *testing.T) {
	unavailable, err := mcpauth.NewAuthenticator(mcpauth.ValidatorFunc(func(context.Context, string) error {
		return mcpauth.ErrValidationUnavailable
	}), mcpauth.UnmeteredOptions("test-outage"))
	require.NoError(t, err)

	for name, handle := range map[string]func(*Telegram, *userStat, string){
		"ask_user": func(tg *Telegram, us *userStat, text string) {
			tg.askUserTokenHandler(context.Background(), us, &tb.Message{Text: text, Sender: us.user})
		},
		"upload": func(tg *Telegram, us *userStat, text string) {
			tg.uploadAuthHandler(context.Background(), us, &tb.Message{Text: "1 - " + text, Sender: us.user})
		},
	} {
		t.Run(name, func(t *testing.T) {
			tg, bot := newRecordingTelegram(t, unavailable)
			us := &userStat{user: &tb.User{ID: 42, Username: "tester"}}
			tg.userStats.Store(us.user.ID, us)

			handle(tg, us, "sk-maybe-valid")
			require.Contains(t, bot.last(t), "temporarily unavailable")
			_, waiting := tg.userStats.Load(us.user.ID)
			require.True(t, waiting, "the user may retry the same key")

			var calls atomic.Int64
			tg.keyAuthenticator = staticAuthenticator(authtest.NewIssuedOnly(&calls))
			handle(tg, us, "sk-rejected")
			require.Contains(t, bot.last(t), "Invalid OneAPI API key")
			require.Equal(t, int64(1), calls.Load())
		})
	}
}
