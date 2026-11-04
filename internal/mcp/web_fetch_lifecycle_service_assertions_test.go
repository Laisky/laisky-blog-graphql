package mcp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// serviceLifecycleExpectation describes terminal state and consumer body acceptance for one synthetic task.
type serviceLifecycleExpectation struct {
	status   string
	accepted bool
	provider bool
}

// serviceLifecycleRows reads only fixture JSON log lines and returns their decoded scalar fields.
func serviceLifecycleRows(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var rows []map[string]any
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var row map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &row))
		rows = append(rows, row)
	}
	return rows
}

// assertServiceLifecycleEvidence joins exact task/request IDs without treating prepared responses as client receipt.
func assertServiceLifecycleEvidence(t *testing.T, workerPath, callerPath string, expected map[string]serviceLifecycleExpectation) {
	t.Helper()
	workerRows, callerRows := serviceLifecycleRows(t, workerPath), serviceLifecycleRows(t, callerPath)
	for taskID, want := range expected {
		processing, published, observed, verified, accepted, prepared := -1, -1, -1, -1, -1, -1
		providerAcquired := -1
		requestID := ""
		for i, row := range workerRows {
			if row["task_id"] != taskID {
				continue
			}
			if row["msg"] == "crawler provider content acquired" {
				require.True(t, want.provider)
				require.Equal(t, -1, providerAcquired)
				providerAcquired = i
				require.Equal(t, "provider", row["acquisition_method"])
				require.Equal(t, "service-reader", row["provider"])
			}
			switch row["event"] {
			case "processing_completed":
				require.Equal(t, -1, processing)
				processing = i
				require.Equal(t, want.status, row["result_status"])
				require.Equal(t, "active", row["worker_context_state"])
			case "result_published":
				require.Equal(t, -1, published)
				published = i
				require.Equal(t, want.status, row["result_status"])
				require.Equal(t, "success", row["publication_outcome"])
			default:
				continue
			}
			require.NotContains(t, row, "caller_receipt")
			duration, ok := row["duration_ms"].(float64)
			require.True(t, ok)
			require.GreaterOrEqual(t, duration, float64(0))
			require.LessOrEqual(t, duration, float64(300000))
		}
		require.GreaterOrEqual(t, processing, 0, "worker processing event for %s", taskID)
		require.Greater(t, published, processing, "terminal publication follows processing locally")
		if want.provider {
			require.GreaterOrEqual(t, providerAcquired, 0, "worker records the actual provider for %s", taskID)
			require.Less(t, providerAcquired, processing)
		} else {
			require.Equal(t, -1, providerAcquired)
		}
		for i, row := range callerRows {
			if row["task_id"] != taskID {
				continue
			}
			switch row["event"] {
			case "result_observed":
				if row["status"] != want.status {
					continue
				}
				require.Equal(t, -1, observed)
				observed = i
				var ok bool
				requestID, ok = row["request_id"].(string)
				require.True(t, ok)
				parsed, err := uuid.Parse(requestID)
				require.NoError(t, err)
				require.NotEqual(t, uuid.Nil, parsed)
			case "result_verified":
				require.Equal(t, -1, verified)
				verified = i
				require.Equal(t, "direct", row["acquisition_method"])
				require.Empty(t, row["provider"])
			case "result_accepted":
				require.Equal(t, -1, accepted)
				accepted = i
				require.Equal(t, "provider", row["acquisition_method"])
				require.Equal(t, "service-reader", row["provider"])
			default:
				continue
			}
			require.Equal(t, "active", row["caller_context_state"])
			duration, ok := row["duration_ms"].(float64)
			require.True(t, ok)
			require.GreaterOrEqual(t, duration, float64(0))
			require.LessOrEqual(t, duration, float64(86400000))
			require.NotContains(t, row, "caller_receipt")
		}
		require.GreaterOrEqual(t, observed, 0, "consumer observes the exact published task")
		for i, row := range callerRows {
			if row["request_id"] == requestID && row["event"] == "response_prepared" {
				require.Equal(t, -1, prepared)
				prepared = i
				require.NotContains(t, row, "caller_receipt")
			}
		}
		if want.accepted {
			if want.provider {
				require.Equal(t, -1, verified, "provider result must never claim a verified direct acquisition")
				require.Greater(t, accepted, observed, "provider acceptance follows observation")
				require.Greater(t, prepared, accepted, "prepared response follows provider acceptance")
			} else {
				require.Equal(t, -1, accepted)
				require.Greater(t, verified, observed, "accepted direct body follows observation")
				require.Greater(t, prepared, verified, "prepared tool response follows body verification")
			}
		} else {
			require.Equal(t, -1, verified, "failed or rejected body is not accepted")
			require.Equal(t, -1, accepted, "failed or rejected provider body is not accepted")
			require.Equal(t, -1, prepared, "failed or rejected body has no successful prepared response")
		}
	}
	// Cross-process log timestamps can race after Redis SET; only causal boundaries and local ordering are asserted.
}
