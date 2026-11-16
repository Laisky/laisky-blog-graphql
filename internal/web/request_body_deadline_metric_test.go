package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
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

// TestRequestBodyPolicyCountsUnarmedDeadlines (maintainer review of PR #95,
// P3): a gin route whose writer cannot arm the read deadline still serves the
// request with only the byte cap, and the request is counted under its
// route class so an operator sees the slow-body protection is off there.
func TestRequestBodyPolicyCountsUnarmedDeadlines(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(newRequestBodyPolicy(urlPrefixConfig{}, loadRequestBodySettings(nil)).middleware())
	engine.POST("/query", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	before := unarmedDeadlineCount(t, string(bodyClassGraphQL))

	for range 3 {
		recorder := httptest.NewRecorder() // cannot set a read deadline
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/query", strings.NewReader(`{"query":"{a}"}`)))
		require.Equal(t, http.StatusNoContent, recorder.Code, "an unarmed deadline never refuses the request")
	}
	require.InDelta(t, 3, unarmedDeadlineCount(t, string(bodyClassGraphQL))-before, 0)
}
