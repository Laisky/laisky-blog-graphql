package redis

import (
	"context"
	"net"
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"

	"github.com/stretchr/testify/require"
)

// TestNewHTMLCrawlerTaskDefaultsToMarkdown verifies the default task payload requests markdown output.
func TestNewHTMLCrawlerTaskDefaultsToMarkdown(t *testing.T) {
	t.Parallel()

	task := NewHTMLCrawlerTask("https://example.com")
	require.True(t, task.OutputMarkdown)
}

// TestNewHTMLCrawlerTaskWithOptionsAllowsRawHTML verifies callers can still explicitly request raw HTML.
func TestNewHTMLCrawlerTaskWithOptionsAllowsRawHTML(t *testing.T) {
	t.Parallel()

	task := NewHTMLCrawlerTaskWithOptions("https://example.com", "", false)
	require.False(t, task.OutputMarkdown)
}

type captureQueueHook struct {
	payload string
	calls   int
}

func (h *captureQueueHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unexpected network dial")
	}
}
func (h *captureQueueHook) ProcessHook(_ redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error {
		h.calls++
		switch typed := cmd.(type) {
		case *redis.IntCmd:
			if cmd.Name() == "rpush" {
				args := cmd.Args()
				h.payload = args[2].(string)
			}
			typed.SetVal(1)
		default:
			return errors.New("unexpected synthetic Redis command")
		}
		return nil
	}
}
func (h *captureQueueHook) ProcessPipelineHook(_ redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(context.Context, []redis.Cmder) error { return errors.New("unexpected synthetic Redis pipeline") }
}

// TestAllQueueProducersAdmitAndPin uses a no-network Redis hook to prove every enqueue path preserves the secure contract.
func TestAllQueueProducersAdmitAndPin(t *testing.T) {
	for _, name := range []string{"default", "options", "provided policy"} {
		t.Run(name, func(t *testing.T) {
			db := NewDB(&redis.Options{Addr: "127.0.0.1:1"})
			t.Cleanup(func() { _ = db.db.Close() })
			hook := &captureQueueHook{}
			db.db.AddHook(hook)
			var id string
			var err error
			switch name {
			case "default":
				id, err = db.AddHTMLCrawlerTask(context.Background(), "https://1.1.1.1/doc")
			case "options":
				id, err = db.AddHTMLCrawlerTaskWithOptions(context.Background(), "https://1.1.1.1/doc", "synthetic-provider-secret", true)
			default:
				id, err = db.AddHTMLCrawlerTaskWithEgress(context.Background(), "https://1.1.1.1/doc", "synthetic-provider-secret", false, &CrawlerEgressPolicy{Host: "1.1.1.1", Addresses: []string{"1.1.1.1"}, MaxRedirects: 1})
			}
			require.NoError(t, err)
			require.Len(t, id, 36)
			require.NotEmpty(t, hook.payload)
			require.NotContains(t, hook.payload, "synthetic-provider-secret")
			task, err := NewHTMLCrawlerTaskFromString(hook.payload)
			require.NoError(t, err)
			require.NotNil(t, task.Egress)
			require.Empty(t, task.APIKey)
			_, err = crawleregress.ValidatePolicy(context.Background(), task.Url, *task.Egress)
			require.NoError(t, err)
		})
	}
}

// TestQueueRejectsUnsafePolicyBeforeRedisAccess verifies admission runs before any network or enqueue side effect.
func TestQueueRejectsUnsafePolicyBeforeRedisAccess(t *testing.T) {
	db := NewDB(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = db.db.Close() })
	hook := &captureQueueHook{}
	db.db.AddHook(hook)
	_, err := db.AddHTMLCrawlerTask(context.Background(), "http://127.0.0.1/private")
	require.Error(t, err)
	require.Zero(t, hook.calls)
	_, err = db.AddHTMLCrawlerTaskWithEgress(context.Background(), "https://1.1.1.1/doc", "", false, &CrawlerEgressPolicy{Host: "1.1.1.1", Addresses: []string{"127.0.0.1"}})
	require.Error(t, err)
	require.Zero(t, hook.calls)
}

// TestCrawlerModelPreservesReceiptAndOwnsPins keeps envelope identity and caller mutations isolated.
func TestCrawlerModelPreservesReceiptAndOwnsPins(t *testing.T) {
	p := &CrawlerEgressPolicy{Host: "1.1.1.1", Addresses: []string{"1.1.1.1"}, MaxRedirects: 1}
	task := NewHTMLCrawlerTaskWithEgress("https://1.1.1.1/doc", "", true, p)
	task.EgressReceipt = crawleregress.NewReceipt(task.TaskID, task.Url, *task.Egress)
	task.EgressReceipt.Origins = []crawleregress.Origin{{URL: task.Url, Addresses: task.Egress.Addresses, PeerAddress: "1.1.1.1", Kind: crawleregress.OriginDocument}}
	p.Addresses[0] = "127.0.0.1"
	require.Equal(t, []string{"1.1.1.1"}, task.Egress.Addresses)
	raw, err := task.ToString()
	require.NoError(t, err)
	decoded, err := NewHTMLCrawlerTaskFromString(raw)
	require.NoError(t, err)
	require.Equal(t, task.EgressReceipt, decoded.EgressReceipt)
	require.NoError(t, crawleregress.VerifyReceipt(context.Background(), decoded.TaskID, decoded.Url, *decoded.Egress, decoded.EgressReceipt))
	_, err = NewHTMLCrawlerTaskFromString(`{"api_key":"synthetic-secret","url":"private-user-content",`)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "synthetic-secret")
	require.NotContains(t, err.Error(), "private-user-content")
}
