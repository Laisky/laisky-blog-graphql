package search

import (
	"context"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/search/diagnostics"
)

func TestManagerDiagnosticsPreserveCauseAndRedact(t *testing.T) {
	canary := errors.New("private-error-query-url-key-canary")
	first := &testEngine{name: "private-engine-name-canary", engineType: "google", err: canary}
	second := &testEngine{name: "fallback", engineType: "firecrawl", err: diagnostics.HTTP(429)}
	manager, err := NewManager([][]Engine{{first}, {second}})
	require.NoError(t, err)
	_, err = manager.Search(context.Background(), "private-query-canary")
	require.Error(t, err)
	require.ErrorIs(t, err, canary, "failover must preserve typed causes")
	require.NotContains(t, err.Error(), "canary")
	d := diagnostics.Summary(err)
	require.Equal(t, "unknown", d.Category, "mixed failures do not attribute the whole operation to the last provider")
	require.Equal(t, "manager", d.Stage)
	require.Zero(t, d.Status)
	require.Equal(t, 2, d.Attempts)
}

type endingContextEngine struct {
	cancel func()
	cause  error
	calls  int
}

func (e *endingContextEngine) Name() string { return "first" }
func (e *endingContextEngine) Type() string { return "google" }
func (e *endingContextEngine) Search(context.Context, string) ([]SearchResultItem, error) {
	e.calls++
	e.cancel()
	return nil, e.cause
}

func TestManagerDiagnosticsStopCancellation(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		ctx, cancel := context.WithCancel(context.Background())
		end := cancel
		if cause == context.DeadlineExceeded {
			ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
			end = func() { <-ctx.Done() }
		}
		t.Cleanup(cancel)
		first := &endingContextEngine{cancel: end, cause: errors.Wrap(cause, "private-canary")}
		second := &testEngine{name: "fallback"}
		manager, err := NewManager([][]Engine{{first}, {second}})
		require.NoError(t, err)
		_, err = manager.Search(ctx, "query")
		require.ErrorIs(t, err, cause)
		require.Equal(t, 1, first.calls)
		require.Zero(t, second.calls, "a cancelled operation must not invoke another paid backend")
		require.Equal(t, 1, diagnostics.Summary(err).Attempts)
	}
}

func TestManagerCallerTerminationRetainsEarlierCause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cause := errors.New("private-cause-canary")
	first := &endingContextEngine{cancel: cancel, cause: cause}
	fallback := &testEngine{name: "never"}
	manager, err := NewManager([][]Engine{{first}, {fallback}})
	require.NoError(t, err)
	_, err = manager.Search(ctx, "query")
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, cause)
	require.Zero(t, fallback.calls)
	require.Equal(t, "canceled", diagnostics.Summary(err).Category)
	require.Equal(t, 1, diagnostics.Summary(err).Attempts)
	require.NotContains(t, err.Error(), "canary")
}

func TestManagerDiagnosticsMixedProviderTermination(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		first := &testEngine{name: "first", engineType: "google", err: diagnostics.HTTP(429)}
		last := &testEngine{name: "last", engineType: "firecrawl", err: cause}
		manager, err := NewManager([][]Engine{{first}, {last}})
		require.NoError(t, err)
		ctx := context.Background()
		_, err = manager.Search(ctx, "synthetic")
		require.NoError(t, ctx.Err())
		require.ErrorIs(t, err, cause)
		d := diagnostics.Summary(err)
		require.Equal(t, "manager", d.Stage)
		require.Equal(t, "unknown", d.Category)
		require.Zero(t, d.Status)
		require.Equal(t, 2, d.Attempts)
	}
}

func TestManagerDiagnosticsCancellationBeforeDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	engine := &testEngine{name: "never"}
	manager, err := NewManager([][]Engine{{engine}})
	require.NoError(t, err)
	_, err = manager.Search(ctx, "query")
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, engine.calls)
	require.Zero(t, diagnostics.Summary(err).Attempts)
}
