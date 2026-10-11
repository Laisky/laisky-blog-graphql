package search

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

type syntheticCrawlerStore struct {
	task                 *rlibs.HTMLCrawlerTask
	mutate               func(*rlibs.HTMLCrawlerTask)
	submitErr, resultErr error
	nilResult            bool
}

func (s *syntheticCrawlerStore) AddHTMLCrawlerTaskWithEgress(_ context.Context, target, key string, markdown bool, policy *rlibs.CrawlerEgressPolicy) (string, error) {
	if s.submitErr != nil {
		return "", s.submitErr
	}
	s.task = rlibs.NewHTMLCrawlerTaskWithEgress(target, key, markdown, policy)
	s.task.Status = rlibs.TaskStatusSuccess
	s.task.ResultHTML = []byte("safe HTML")
	s.task.ResultMarkdown = []byte("safe markdown")
	s.task.EgressReceipt = crawleregress.NewReceipt(s.task.TaskID, target, *policy)
	s.task.EgressReceipt.Origins = []crawleregress.Origin{{URL: target, Addresses: policy.Addresses, PeerAddress: "1.1.1.1", Kind: crawleregress.OriginDocument}}
	id := s.task.TaskID
	if s.mutate != nil {
		s.mutate(s.task)
	}
	return id, nil
}
func (s *syntheticCrawlerStore) GetHTMLCrawlerTaskResult(context.Context, string) (*rlibs.HTMLCrawlerTask, error) {
	if s.nilResult {
		return nil, s.resultErr
	}
	return s.task, s.resultErr
}

// TestRenderedTaskBoundEvidence reproduces success/body decisions without a real Redis or remote fetch.
func TestRenderedTaskBoundEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*rlibs.HTMLCrawlerTask)
		code   string
		body   string
	}{
		{"verified markdown", nil, "", "safe markdown"},
		{"changed output format", func(task *rlibs.HTMLCrawlerTask) { task.OutputMarkdown = false }, "crawler_egress_rejected", ""},
		{"empty markdown", func(task *rlibs.HTMLCrawlerTask) { task.ResultMarkdown = nil }, "crawler_result_failed", ""},
		{"missing evidence", func(task *rlibs.HTMLCrawlerTask) { task.EgressReceipt = nil }, "crawler_egress_unverified", ""},
		{"URL only claimed evidence", func(task *rlibs.HTMLCrawlerTask) { task.EgressReceipt = nil; task.RequestChain = []string{task.Url} }, "crawler_egress_unverified", ""},
		{"mismatched task", func(task *rlibs.HTMLCrawlerTask) { task.TaskID = "different" }, "crawler_egress_rejected", ""},
		{"mismatched target", func(task *rlibs.HTMLCrawlerTask) { task.Url = "https://1.1.1.1/other" }, "crawler_egress_rejected", ""},
		{"mismatched policy", func(task *rlibs.HTMLCrawlerTask) { task.Egress.MaxRedirects++ }, "crawler_egress_rejected", ""},
		{"forged peer", func(task *rlibs.HTMLCrawlerTask) { task.EgressReceipt.Origins[0].PeerAddress = "127.0.0.1" }, "crawler_egress_rejected", ""},
		{"failed renderer", func(task *rlibs.HTMLCrawlerTask) {
			task.Status = rlibs.TaskStatusFailed
			secret := "synthetic credential https://private.test/user-content"
			task.FailedReason = &secret
		}, "crawler_failed", ""},
		{"unknown state", func(task *rlibs.HTMLCrawlerTask) { task.Status = "untrusted synthetic state" }, "crawler_status_invalid", ""},
		{"old worker drops contract", legacyWorkerRoundTrip, "crawler_egress_rejected", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &syntheticCrawlerStore{mutate: tc.mutate}
			body, err := fetchDynamicURLContent(context.Background(), store, "https://1.1.1.1/doc", "synthetic-provider-secret", true)
			if tc.code == "" {
				require.NoError(t, err)
				require.Empty(t, store.task.APIKey)
				require.Equal(t, tc.body, string(body))
				return
			}
			require.Error(t, err)
			require.Empty(t, body)
			diag := FetchDiagnostic(err)
			require.Equal(t, tc.code, diag.ErrorCode)
			require.Len(t, diag.TaskID, 36)
			require.NotContains(t, err.Error(), "synthetic credential")
			require.NotContains(t, err.Error(), "user-content")
		})
	}
}

// legacyWorkerRoundTrip models the deployed May schema discarding egress fields.
func legacyWorkerRoundTrip(task *rlibs.HTMLCrawlerTask) {
	raw, _ := json.Marshal(task)
	var old struct {
		TaskID     string `json:"task_id"`
		URL        string `json:"url"`
		Status     string `json:"status"`
		ResultHTML []byte `json:"result_html"`
	}
	_ = json.Unmarshal(raw, &old)
	raw, _ = json.Marshal(old)
	*task = rlibs.HTMLCrawlerTask{}
	_ = json.Unmarshal(raw, task)
}

// TestFetchFailureIdentity verifies safe metadata retains cancellation and queue errors.
func TestFetchFailureIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store *syntheticCrawlerStore
		code  string
		cause error
	}{
		{"nil result", &syntheticCrawlerStore{nilResult: true}, "crawler_result_failed", nil},
		{"submit error", &syntheticCrawlerStore{submitErr: errors.New("synthetic secret")}, "crawler_submit_failed", nil},
		{"result error", &syntheticCrawlerStore{resultErr: errors.New("synthetic secret")}, "crawler_result_failed", nil},
		{"result timeout", &syntheticCrawlerStore{resultErr: context.DeadlineExceeded}, "fetch_timeout", context.DeadlineExceeded},
		{"result cancellation", &syntheticCrawlerStore{resultErr: context.Canceled}, "fetch_canceled", context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := fetchDynamicURLContent(context.Background(), tc.store, "https://1.1.1.1/doc", "synthetic-provider-secret", true)
			require.Empty(t, body)
			require.Error(t, err)
			require.Equal(t, tc.code, FetchDiagnostic(err).ErrorCode)
			require.NotContains(t, err.Error(), "synthetic secret")
			if tc.cause != nil {
				require.ErrorIs(t, err, tc.cause)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body, err := fetchDynamicURLContent(ctx, &syntheticCrawlerStore{}, "https://1.1.1.1/doc", "synthetic-provider-secret", true)
	require.Empty(t, body)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "fetch_canceled", FetchDiagnostic(err).ErrorCode)
}

// TestFetchDiagnosticRejectsUnreviewedFields keeps task and error canaries out of alert metadata.
func TestFetchDiagnosticRejectsUnreviewedFields(t *testing.T) {
	diag := FetchDiagnostic(&FetchFailureError{Diagnostic: FetchFailureDiagnostic{ErrorCode: "https://private.test/token", Stage: "user content", TaskID: "synthetic secret"}, Cause: errors.New("synthetic secret")})
	require.Equal(t, FetchFailureDiagnostic{ErrorCode: "fetch_failed", Stage: "fetch"}, diag)
}

// TestVerifiedRawHTMLSuccess ensures explicit HTML mode returns the verified document.
func TestVerifiedRawHTMLSuccess(t *testing.T) {
	store := &syntheticCrawlerStore{}
	body, err := fetchDynamicURLContent(context.Background(), store, "https://1.1.1.1/doc", "synthetic-provider-secret", false)
	require.NoError(t, err)
	require.Equal(t, "safe HTML", string(body))
	require.Empty(t, store.task.APIKey)
}
