package telegram

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
)

// slowReadDeadline is how long a test lets the misses that may not wait take to
// answer. It is well under alertCredentialLoadTimeout, which used to be the only
// thing that released them.
const slowReadDeadline = alertCredentialLoadTimeout / 2

// readDirectory runs one directory read to completion, as a miss that waits does.
func readDirectory(d *alertCredentialDirectory) {
	if done := d.startRead(context.Background(), glog.Shared); done != nil {
		<-done
	}
}

// heldDirectoryRead makes d's reads block until the returned release is called,
// and returns a count of the reads that started.
func heldDirectoryRead(t *testing.T, d *alertCredentialDirectory) (release func(), reads *atomic.Int32) {
	t.Helper()
	gate := make(chan struct{})
	reads = new(atomic.Int32)
	load := d.load
	d.load = func(ctx context.Context, limit int) ([]*model.AlertTypes, bool, error) {
		reads.Add(1)
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, false, errors.WithStack(ctx.Err())
		}
		return load(ctx, limit)
	}
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return release, reads
}

// TestAlertCredentialDirectoryBoundsReadWaiters reproduces unbounded waiting
// before fallback admission. Every miss during a slow directory read joined the
// shared read, and singleflight.DoChan kept a channel per waiter until the read
// ended, so a flood of misses held any number of requests for up to the read
// timeout before the bounded fallback lookup could refuse them. Now at most
// maxAlertCredentialReadWaiters misses wait; the rest answer at once from the
// copy as it stands. One read still serves them all, and the misses that waited
// see the copy it produced.
func TestAlertCredentialDirectoryBoundsReadWaiters(t *testing.T) {
	directory := newAlertCredentialDirectory(func(context.Context, int) ([]*model.AlertTypes, bool, error) {
		return []*model.AlertTypes{{Name: "valid", PushToken: "valid-token"}}, true, nil
	}, 10, time.Hour)
	release, reads := heldDirectoryRead(t, directory)
	var output bytes.Buffer
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.Lock(zapcore.AddSync(&output)), zap.DebugLevel)
	logger, err := glog.NewWithName("alert_credential_waiters_test", glog.LevelDebug,
		zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)

	const callers = 4 * maxAlertCredentialReadWaiters
	verdicts := make([]credentialVerdict, callers)
	var answered atomic.Int32
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			verdicts[i] = directory.check(context.Background(), logger, "valid", "valid-token")
			answered.Add(1)
		})
	}
	require.Eventually(t, func() bool { return answered.Load() >= callers-maxAlertCredentialReadWaiters },
		slowReadDeadline, time.Millisecond, "misses past the waiting limit must not wait for a slow read")
	require.EqualValues(t, callers-maxAlertCredentialReadWaiters, answered.Load(),
		"misses within the waiting limit still wait for the read")

	release()
	wg.Wait()
	require.EqualValues(t, 1, reads.Load(), "waiting and non-waiting misses share one read")
	waited := 0
	for i, verdict := range verdicts {
		if verdict == credentialKnown {
			waited++
			continue
		}
		require.Equal(t, credentialUnverifiable, verdict, "a miss that did not wait answers from the empty copy: %d", i)
	}
	require.Equal(t, maxAlertCredentialReadWaiters, waited, "a miss that waited sees the new copy")
	require.Equal(t, credentialKnown, directory.check(context.Background(), glog.Shared, "valid", "valid-token"))

	// Misses past the limit are counted, and warned about once per interval, so a
	// flood cannot set the log volume.
	require.NoError(t, logger.Sync())
	require.EqualValues(t, callers-maxAlertCredentialReadWaiters, directory.overflows.Load())
	require.Equal(t, 1, strings.Count(output.String(), "more waiting misses than allowed"), output.String())
	require.Contains(t, output.String(), `"waiting_limit":`+strconv.Itoa(maxAlertCredentialReadWaiters))
}

// TestAlertEntrypointsBoundWaitingOnSlowDirectoryRead reproduces the same
// defect through both public entrypoints: during a slow directory read, unknown
// credentials sent to the GraphQL mutation or the Cloud Monitoring webhook were
// held until the read ended instead of reaching the bounded fallback lookup.
// Now all but the waiting limit are answered at once with the fallback's
// retryable refusal or storage's credential refusal, storage sees no more
// lookups than the fallback allows, and a known credential still delivers.
func TestAlertEntrypointsBoundWaitingOnSlowDirectoryRead(t *testing.T) {
	configureAlertThrottle(t, 100, 100, 100, 100)
	svc := &tokenGatedTelegram{tokens: map[string]string{"known_alert": "known-token"}}
	resolver := NewMutationResolver(svc)
	resolver.credentials.fallback = rate.NewLimiter(0, 5)
	webhook := resolver.CloudMonitoringWebhook()
	release, reads := heldDirectoryRead(t, resolver.credentials)

	const callers = 4 * maxAlertCredentialReadWaiters
	graphQLErrs := make([]error, callers)
	webhookCodes := make([]int, callers)
	var answered atomic.Int32
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			defer answered.Add(1)
			alertType := "probe-" + strconv.Itoa(i)
			if i%2 == 0 {
				_, graphQLErrs[i] = resolver.TelegramMonitorAlert(context.Background(), alertType, "guess", "flood")
				return
			}
			request := httptest.NewRequest(http.MethodPost, "/webhook/gcp/alerts/cloud-monitoring", strings.NewReader(openIncidentBody))
			request.SetBasicAuth(alertType, "guess")
			recorder := httptest.NewRecorder()
			webhook.ServeHTTP(recorder, request)
			webhookCodes[i] = recorder.Code
		})
	}
	require.Eventually(t, func() bool { return answered.Load() >= callers-maxAlertCredentialReadWaiters },
		slowReadDeadline, time.Millisecond, "unknown credentials past the waiting limit must reach the bounded fallback at once")

	release()
	wg.Wait()
	require.EqualValues(t, 1, reads.Load())
	require.LessOrEqual(t, svc.lookups(), 5, "storage lookups stay within the fallback budget")
	for i := range callers {
		if i%2 == 0 {
			err := graphQLErrs[i]
			require.Error(t, err, i)
			require.True(t, errors.Is(err, errAlertThrottled) || errors.Is(err, service.ErrAlertUnauthorized), "graphql %d: %v", i, err)
			continue
		}
		require.Contains(t, []int{http.StatusServiceUnavailable, http.StatusUnauthorized}, webhookCodes[i], "webhook %d", i)
	}

	_, err := resolver.TelegramMonitorAlert(t.Context(), "known_alert", "known-token", "genuine")
	require.NoError(t, err, "a known credential still delivers after the flood")
}
