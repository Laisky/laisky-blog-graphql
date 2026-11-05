package telegram

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
)

// guessingRounds is how many wrong guesses a test sends to spend any budget a
// receiver might keep for refused credentials.
const guessingRounds = 4 * 20

// guess sends n wrong guesses for alertType and returns the statuses seen.
func guess(t *testing.T, url, alertType string, n int) map[int]int {
	t.Helper()
	statuses := make(map[int]int)
	for i := range n {
		statuses[postWebhook(t, url, alertType, "wrong-guess-"+strconv.Itoa(i), openIncidentBody).status]++
	}
	return statuses
}

// TestCloudMonitoringGuessingCannotLockOutValidChannels proves that wrong guesses
// from someone else never keep a valid channel from delivering. The attacker
// knows the alert type names, since TelegramAlertTypes lists them, and can
// create alert types through the bot, but holds no channel's push token.
func TestCloudMonitoringGuessingCannotLockOutValidChannels(t *testing.T) {
	t.Run("a channel's first delivery after a restart", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := new(webhookTelegram)
		server, _, _ := startClockedWebhook(t, svc)
		guess(t, server.URL, webhookAlertType, guessingRounds)
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status,
			"every deploy restarts the gateway, so a channel's first delivery must not wait on someone else's guesses")
	})

	t.Run("a guesser below any edge threshold", func(t *testing.T) {
		// Ten guesses a second is 100 per 10 s, below an edge threshold that must
		// pass Monitoring's 1,000-notification bursts.
		enableAlertThrottle(t)
		svc := new(webhookTelegram)
		server, _, clock := startClockedWebhook(t, svc)
		statuses := make(map[int]int)
		for slot := range 600 {
			guess(t, server.URL, webhookAlertType, 1)
			// The channel retries every 10 s, each time right after a guess.
			if slot%100 == 99 {
				statuses[postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status]++
			}
			clock.advance(100 * time.Millisecond)
		}
		require.Equal(t, map[int]int{http.StatusNoContent: 6}, statuses)
	})

	t.Run("a rotated token", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := new(webhookTelegram)
		server, _, _ := startClockedWebhook(t, svc)
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status)
		const rotated = "synthetic-rotated-push-token"
		svc.rotate(rotated)
		guess(t, server.URL, "other", guessingRounds)
		require.Equal(t, http.StatusUnauthorized, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status,
			"revocation takes effect at once")
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, webhookAlertType, rotated, openIncidentBody).status,
			"the new token must deliver even while someone is guessing")
	})

	t.Run("self-service alert types cannot crowd a channel out", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := new(webhookTelegram)
		server, _, _ := startClockedWebhook(t, svc)
		// Each attacker-created type authenticates once. The empty body is
		// refused after authentication, so no send budget is spent.
		for i := range 300 {
			svc.addAlertType("crowd-"+strconv.Itoa(i), "crowd-token-"+strconv.Itoa(i))
		}
		for i := range 300 {
			require.Equal(t, http.StatusBadRequest, postWebhook(t, server.URL, "crowd-"+strconv.Itoa(i), "crowd-token-"+strconv.Itoa(i), `{}`).status)
		}
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status)
		guess(t, server.URL, webhookAlertType, guessingRounds)
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status)
	})
}

// TestCloudMonitoringRefusalsBoundStorage proves guesses use only the separate
// fallback budget and exhaustion returns a retryable status.
func TestCloudMonitoringRefusalsBoundStorage(t *testing.T) {
	enableAlertThrottle(t)
	svc := new(webhookTelegram)
	server, handler, _ := startClockedWebhook(t, svc)
	handler.credentials.fallback = rate.NewLimiter(0, 5)
	require.Equal(t, map[int]int{http.StatusUnauthorized: 5, http.StatusServiceUnavailable: guessingRounds - 5},
		guess(t, server.URL, webhookAlertType, guessingRounds))
	require.Equal(t, map[int]int{http.StatusServiceUnavailable: guessingRounds}, guess(t, server.URL, "unknown", guessingRounds))
	require.Equal(t, 5, svc.credentialChecks(), "guesses must not exceed the fallback budget")
	require.Equal(t, http.StatusUnauthorized, postWebhook(t, server.URL, "", "wrong", openIncidentBody).status)
}

// TestCloudMonitoringDirectoryReads pins when the credential directory reads
// storage: once per interval at most, whatever the request rate, and never for
// the credential-free challenge. Every count is exact on the test clock.
func TestCloudMonitoringDirectoryReads(t *testing.T) {
	t.Run("guessing costs one read per interval", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := new(webhookTelegram)
		server, handler, clock := startClockedWebhook(t, svc)
		handler.credentials.fallback = rate.NewLimiter(0, 5)
		guess(t, server.URL, webhookAlertType, guessingRounds)
		require.Equal(t, 1, svc.directoryLoads())
		clock.advance(alertCredentialRefreshInterval - time.Nanosecond)
		guess(t, server.URL, "unknown", guessingRounds)
		require.Equal(t, 1, svc.directoryLoads(), "a current copy avoids another directory read")
		clock.advance(time.Nanosecond)
		guess(t, server.URL, "unknown", guessingRounds)
		require.Equal(t, 2, svc.directoryLoads())
		require.Equal(t, 5, svc.credentialChecks())
	})

	t.Run("challenges and oversized credentials never read", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := new(webhookTelegram)
		server, _, _ := startClockedWebhook(t, svc)
		require.Equal(t, http.StatusUnauthorized, postWebhook(t, server.URL, "", "", openIncidentBody).status)
		oversized := strings.Repeat("x", maxCloudMonitoringCredentialBytes+1)
		require.Equal(t, http.StatusUnauthorized, postWebhook(t, server.URL, webhookAlertType, oversized, openIncidentBody).status)
		require.Equal(t, http.StatusUnauthorized, postWebhook(t, server.URL, oversized, webhookToken, openIncidentBody).status)
		require.Equal(t, http.StatusUnauthorized, postWebhook(t, server.URL, " ", " ", openIncidentBody).status)
		require.Zero(t, svc.directoryLoads())
		require.Zero(t, svc.credentialChecks())
	})

	t.Run("a known credential is checked against storage every time", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := new(webhookTelegram)
		server, _, _ := startClockedWebhook(t, svc)
		for range 5 {
			require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status)
		}
		require.Equal(t, 1, svc.directoryLoads())
		require.Equal(t, 5, svc.credentialChecks(), "the copy never authenticates on its own")
	})

	t.Run("padded credentials match as storage matches them", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := new(webhookTelegram)
		server, _, _ := startClockedWebhook(t, svc)
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, " "+webhookAlertType+"\t", " "+webhookToken+" ", openIncidentBody).status)
	})
}

// TestCloudMonitoringDirectoryFollowsStorage checks bounded fallback and that
// the copy never authenticates without storage.
func TestCloudMonitoringDirectoryFollowsStorage(t *testing.T) {
	t.Run("a new alert type is accepted before the interval passes", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := new(webhookTelegram)
		server, _, clock := startClockedWebhook(t, svc)
		guess(t, server.URL, webhookAlertType, 1)
		svc.addAlertType("fresh", "fresh-token")
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, "fresh", "fresh-token", openIncidentBody).status)
		clock.advance(alertCredentialRefreshInterval - time.Nanosecond)
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, "fresh", "fresh-token", openIncidentBody).status)
		clock.advance(time.Nanosecond)
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, "fresh", "fresh-token", openIncidentBody).status)
	})

	t.Run("a refusal by storage makes the next unknown credential reread", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := new(webhookTelegram)
		server, _, _ := startClockedWebhook(t, svc)
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status)
		svc.rotate("synthetic-rotated-push-token")
		require.Equal(t, http.StatusUnauthorized, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status)
		require.Equal(t, http.StatusUnauthorized, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status)
		require.Equal(t, 3, svc.credentialChecks(), "an unknown pair uses the bounded authoritative fallback")
		require.Equal(t, 2, svc.directoryLoads())
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, webhookAlertType, "synthetic-rotated-push-token", openIncidentBody).status)
	})

	t.Run("a failed read uses fallback and is retried after a second", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := &webhookTelegram{loadErr: errors.New("mongo unavailable")}
		server, _, clock := startClockedWebhook(t, svc)
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status)
		require.Equal(t, http.StatusUnauthorized, postWebhook(t, server.URL, webhookAlertType, "wrong", openIncidentBody).status)
		require.Equal(t, 1, svc.directoryLoads())
		svc.mu.Lock()
		svc.loadErr = nil
		svc.mu.Unlock()
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status,
			"the pair the fallback accepted is remembered")
		require.Equal(t, http.StatusUnauthorized, postWebhook(t, server.URL, webhookAlertType, "wrong", openIncidentBody).status)
		require.Equal(t, 1, svc.directoryLoads(), "no reread within a second of the failed one")
		clock.advance(alertCredentialRetryInterval)
		require.Equal(t, http.StatusUnauthorized, postWebhook(t, server.URL, webhookAlertType, "wrong", openIncidentBody).status)
		require.Equal(t, 2, svc.directoryLoads(), "the next miss after a second retries the read")
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status)
	})

	t.Run("an outage is not a revocation", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := new(webhookTelegram)
		server, _, clock := startClockedWebhook(t, svc)
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status)
		svc.mu.Lock()
		svc.loadErr, svc.validateErr = errors.New("mongo unavailable"), errors.New("mongo unavailable")
		svc.mu.Unlock()
		clock.advance(alertCredentialRefreshInterval)
		require.Equal(t, map[int]int{http.StatusServiceUnavailable: 1}, guess(t, server.URL, "unknown", 1),
			"a copy that could not be reread refuses nothing")
		require.Equal(t, http.StatusServiceUnavailable, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status)
		svc.mu.Lock()
		svc.validateErr = nil
		svc.mu.Unlock()
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status,
			"a failed read keeps the credentials the copy already knew")
	})

	t.Run("a truncated directory uses bounded fallback", func(t *testing.T) {
		enableAlertThrottle(t)
		svc := new(webhookTelegram)
		svc.addAlertType("a-older", "token-a")
		svc.addAlertType("b-newer", "token-b")
		server, handler, _ := startClockedWebhook(t, svc)
		handler.credentials.limit = 2
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, webhookAlertType, webhookToken, openIncidentBody).status)
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, "a-older", "token-a", openIncidentBody).status)
		require.Equal(t, http.StatusNoContent, postWebhook(t, server.URL, "b-newer", "token-b", openIncidentBody).status)
		require.Equal(t, http.StatusUnauthorized, postWebhook(t, server.URL, "unknown", "guess", openIncidentBody).status)
		require.Equal(t, 4, svc.credentialChecks())
	})
}

// TestAlertCredentialDirectorySharesOneRead proves concurrent misses within the
// waiting limit wait for a single read, so neither a cold start nor a guessing
// burst multiplies storage reads, and a valid credential arriving mid-read is
// not answered from the copy being replaced. Misses past the limit are covered
// by TestAlertCredentialDirectoryBoundsReadWaiters.
func TestAlertCredentialDirectorySharesOneRead(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	reads := 0
	directory := newAlertCredentialDirectory(func(ctx context.Context, _ int) ([]*model.AlertTypes, bool, error) {
		mu.Lock()
		reads++
		mu.Unlock()
		select {
		case <-release:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
		return []*model.AlertTypes{{Name: "valid", PushToken: "valid-token"}}, true, nil
	}, 10, time.Hour)
	logger := glog.Shared

	// One waiting place stays free for the canceled caller below.
	const callers = maxAlertCredentialReadWaiters - 1
	verdicts := make([]credentialVerdict, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			token := "valid-token"
			if i%2 == 1 {
				token = "guess-" + strconv.Itoa(i)
			}
			verdicts[i] = directory.check(context.Background(), logger, "valid", token)
		})
	}
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return reads == 1
	}, 5*time.Second, time.Millisecond)

	// A caller whose request ends stops waiting, and the read goes on.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.Equal(t, credentialUnverifiable, directory.check(canceled, logger, "valid", "valid-token"))

	close(release)
	wg.Wait()
	for i, verdict := range verdicts {
		want := credentialKnown
		if i%2 == 1 {
			want = credentialUnknown
		}
		require.Equal(t, want, verdict, i)
	}
	require.Equal(t, 1, reads)
}

// TestAlertCredentialDirectoryHoldsOnlyDigests keeps push tokens, and any hash
// of one that could be checked offline, out of memory.
func TestAlertCredentialDirectoryHoldsOnlyDigests(t *testing.T) {
	directory := newAlertCredentialDirectory(func(context.Context, int) ([]*model.AlertTypes, bool, error) {
		return []*model.AlertTypes{{Name: "ab", PushToken: "c-token"}, {Name: "a", PushToken: "bc-token"}}, true, nil
	}, 10, time.Hour)
	require.Equal(t, credentialKnown, directory.check(context.Background(), glog.Shared, "ab", "c-token"))
	require.Equal(t, credentialKnown, directory.check(context.Background(), glog.Shared, "a", "bc-token"))
	require.Equal(t, credentialUnknown, directory.check(context.Background(), glog.Shared, "abc", "-token"),
		"the name's length prefix keeps pairs that concatenate alike apart")
	require.Equal(t, credentialUnknown, directory.check(context.Background(), glog.Shared, "ab", "bc-token"),
		"a token belongs to its own alert type")
	require.Len(t, directory.digests, 2)
	for digest := range directory.digests {
		require.NotContains(t, string(digest[:]), "token")
	}

	directory.forget("ab", "c-token")
	require.Len(t, directory.digests, 1)
	require.True(t, directory.readAt.IsZero(), "a storage refusal lets the next miss reread")
}

// A malformed storage result must not terminate the shared reader or make a
// partial directory authoritative for rejecting credentials.
func TestAlertCredentialDirectoryRejectsNilLoaderEntry(t *testing.T) {
	malformed := false
	directory := newAlertCredentialDirectory(func(context.Context, int) ([]*model.AlertTypes, bool, error) {
		alerts := []*model.AlertTypes{{Name: "valid", PushToken: "valid-token"}}
		if malformed {
			alerts = append(alerts, nil)
		}
		return alerts, true, nil
	}, 10, time.Hour)
	readDirectory(directory)
	malformed = true
	directory.readAt = time.Time{}
	require.NotPanics(t, func() { readDirectory(directory) })
	verdict, _ := directory.lookup(directory.digest("missing", "token"))
	require.Equal(t, credentialUnverifiable, verdict)
	verdict, _ = directory.lookup(directory.digest("valid", "valid-token"))
	require.Equal(t, credentialKnown, verdict, "a failed refresh retains the previous good copy")
	require.Nil(t, directory.readDone, "a failed read ends")
}

// BenchmarkAlertCredentialDirectoryMiss distinguishes a hot miss from a full
// refresh; the latter excludes MongoDB latency and measures hashing and copying.
func BenchmarkAlertCredentialDirectoryMiss(b *testing.B) {
	for _, size := range []int{1000, 10000} {
		alerts := make([]*model.AlertTypes, size)
		for i := range alerts {
			alerts[i] = &model.AlertTypes{Name: "alert-" + strconv.Itoa(i), PushToken: "token-" + strconv.Itoa(i)}
		}
		for _, refresh := range []bool{false, true} {
			name := "hot/"
			if refresh {
				name = "refresh/"
			}
			b.Run(name+strconv.Itoa(size), func(b *testing.B) {
				directory := newAlertCredentialDirectory(func(context.Context, int) ([]*model.AlertTypes, bool, error) {
					return alerts, true, nil
				}, size, time.Hour)
				readDirectory(directory)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if refresh {
						directory.readAt = time.Time{}
					}
					directory.check(context.Background(), glog.Shared, "missing", "token")
				}
			})
		}
	}
}

// TestCloudMonitoringRefusalWarningsAreSampled keeps a guessing caller from
// setting the log volume: refusals are counted, and warned about at most once
// per interval.
func TestCloudMonitoringRefusalWarningsAreSampled(t *testing.T) {
	enableAlertThrottle(t)
	var output bytes.Buffer
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zap.DebugLevel)
	logger, err := glog.NewWithName("cloud_monitoring_test", glog.LevelDebug,
		zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)

	handler := NewCloudMonitoringWebhook(new(webhookTelegram))
	handler.credentials.fallback = rate.NewLimiter(rate.Inf, 50)
	const refused = 50
	for range refused {
		request := httptest.NewRequest(http.MethodPost, "/webhook/gcp/alerts/cloud-monitoring", strings.NewReader(openIncidentBody))
		request = request.WithContext(gmw.SetLogger(request.Context(), logger))
		request.SetBasicAuth(webhookAlertType, "wrong")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusUnauthorized, recorder.Code)
	}
	require.NoError(t, logger.Sync())
	require.Equal(t, int64(refused), handler.refusals.Load())
	require.Equal(t, 1, strings.Count(output.String(), "refuse cloud monitoring credentials"), output.String())
	require.Contains(t, output.String(), `"refused_total":1`)
}
