package search

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"

	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// TestCrawlerFailureContextBoundWire preserves observed zero values only for the
// exact submitted task, target, output mode and immutable egress policy.
func TestCrawlerFailureContextBoundWire(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*rlibs.HTMLCrawlerTask)
		want   bool
	}{
		{"bound", nil, true},
		{"wrong task", func(v *rlibs.HTMLCrawlerTask) { v.TaskID = "0199c118-4229-7000-8000-000000000099" }, false},
		{"wrong target", func(v *rlibs.HTMLCrawlerTask) { v.Url = "https://1.1.1.1/other" }, false},
		{"wrong output", func(v *rlibs.HTMLCrawlerTask) { v.OutputMarkdown = false }, false},
		{"missing policy", func(v *rlibs.HTMLCrawlerTask) { v.Egress = nil }, false},
		{"wrong policy", func(v *rlibs.HTMLCrawlerTask) { v.Egress.MaxRedirects++ }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &syntheticCrawlerStore{mutate: func(v *rlibs.HTMLCrawlerTask) {
				v.Status = rlibs.TaskStatusFailed
				raw, err := json.Marshal(v)
				require.NoError(t, err)
				var wire map[string]any
				require.NoError(t, json.Unmarshal(raw, &wire))
				wire["failure_stage"] = "conversion"
				wire["failure_category"] = "empty_markdown"
				wire["failure_context"] = map[string]any{"upstream_status": 200, "conversion_input_bytes": 123, "conversion_output_bytes": 0, "conversion_output_trimmed_bytes": 0, "converter": "html-to-markdown/v2"}
				raw, err = json.Marshal(wire)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(raw, v))
				if tc.mutate != nil {
					tc.mutate(v)
				}
			}}
			body, err := fetchDynamicURLContent(context.Background(), store, "https://1.1.1.1/doc", "", true)
			require.Error(t, err)
			require.Empty(t, body)
			diagnostic := FetchDiagnostic(err)
			require.Equal(t, "crawler_failed", diagnostic.ErrorCode)
			require.Equal(t, "render", diagnostic.Stage)
			raw, err := json.Marshal(diagnostic)
			require.NoError(t, err)
			var encoded map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &encoded))
			if tc.want {
				require.Contains(t, encoded, "WorkerContext")
				var fields map[string]any
				require.NoError(t, json.Unmarshal(encoded["WorkerContext"], &fields))
				require.EqualValues(t, 0, fields["conversion_output_bytes"])
				require.EqualValues(t, 123, fields["conversion_input_bytes"])
			} else {
				require.NotContains(t, encoded, "WorkerContext")
			}
		})
	}
}

// TestCrawlerFailureContextAdmittedOriginProjection keeps diagnostics bound to
// already admitted Unicode and alternate IPv6 spelling without a second hostname
// normalization rule or a live DNS request.
func TestCrawlerFailureContextAdmittedOriginProjection(t *testing.T) {
	for _, raw := range []string{"https://BÜCHER.example./doc", "https://[2606:4700:4700:0:0:0:0:1111]/doc"} {
		admission, err := crawleregress.AdmitURLWithResolver(context.Background(), raw, func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
		})
		require.NoError(t, err)
		scheme, hostname := crawleregress.TargetComponents(raw)
		require.Equal(t, "https", scheme)
		require.Equal(t, admission.Host, hostname)
		policy := admission.Policy(crawleregress.DefaultMaxRedirects, false)
		task := rlibs.NewHTMLCrawlerTaskWithEgress(raw, "", true, &policy)
		task.Status = rlibs.TaskStatusFailed
		task.FailureStage = "conversion"
		task.FailureCategory = "empty_markdown"
		zero := int64(0)
		task.FailureContext = &crawleregress.FailureContext{TargetScheme: scheme, TargetHostname: hostname, ConversionOutputBytes: &zero}
		diagnostic := FetchDiagnostic(failedCrawlerTask(task.TaskID, raw, policy, true, task))
		require.NotNil(t, diagnostic.WorkerContext)
		require.Equal(t, hostname, diagnostic.WorkerContext.TargetHostname)
		require.Equal(t, zero, *diagnostic.WorkerContext.ConversionOutputBytes)
	}
}
