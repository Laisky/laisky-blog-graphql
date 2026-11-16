package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// unarmedDeadlineCount reads http_request_body_read_deadline_unarmed_total
// for entry from the default registry, which /metrics serves.
func unarmedDeadlineCount(t *testing.T, entry string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "http_request_body_read_deadline_unarmed_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "entry" && label.GetValue() == entry {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// TestBodyBudgetCountsUnarmedDeadlines (maintainer review of PR #95, P3):
// when the transport cannot arm a body's read deadline the request still
// runs with only the byte cap, which used to leave a DEBUG line only. Every
// such request is now counted under entry "mcp", and the condition is logged
// at WARN at most once per interval (DEBUG otherwise).
func TestBodyBudgetCountsUnarmedDeadlines(t *testing.T) {
	core, observed := observer.New(zap.DebugLevel)
	logger, err := glog.NewWithName("body-budget", glog.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)
	served := 0
	handler := withRequestBodyBudget(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served++ }),
		DefaultRequestBodyBudget(), logger)
	before := unarmedDeadlineCount(t, "mcp")

	for range 2 {
		// httptest's recorder cannot set a read deadline.
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp/", strings.NewReader(`{}`)))
	}

	require.Equal(t, 2, served, "an unarmed deadline never refuses the request")
	require.InDelta(t, 2, unarmedDeadlineCount(t, "mcp")-before, 0)
	lines := observed.FilterMessageSnippet("read deadline unavailable").All()
	require.Len(t, lines, 2)
	require.Equal(t, zapcore.WarnLevel, lines[0].Level, "the first occurrence is a WARN")
	require.Equal(t, zapcore.DebugLevel, lines[1].Level, "later ones within the interval are DEBUG")
}
