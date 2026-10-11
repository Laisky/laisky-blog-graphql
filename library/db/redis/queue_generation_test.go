package redis

import (
	"context"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
)

// This literal represents the key compiled into already deployed legacy workers.
// It deliberately does not follow the current shared pending-key constant.
const compiledLegacyCrawlerQueue = "laisky/tasks/html_crawler/pending"

// TestVerifiedCrawlerQueueIsolatesMixedFleet exercises actual producer, dequeue,
// JSON result and verifier paths while an old consumer uses its compiled key.
func TestVerifiedCrawlerQueueIsolatesMixedFleet(t *testing.T) {
	require.Equal(t, compiledLegacyCrawlerQueue, KeyTaskHTMLCrawlerLegacyPending)
	require.Equal(t, "laisky/tasks/html_crawler/verified/v1/pending", KeyTaskHTMLCrawlerPending)
	require.Equal(t, "laisky/tasks/html_crawler/result/", KeyPrefixTaskHTMLCrawlerResult)
	for _, producer := range []string{"default", "options", "provided policy"} {
		t.Run(producer, func(t *testing.T) {
			server := miniredis.RunT(t)
			db := NewDB(&redis.Options{Addr: server.Addr(), Protocol: 2, MaxRetries: -1, DisableIdentity: true})
			t.Cleanup(func() { require.NoError(t, db.GetDB().Close()) })
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			legacy := NewHTMLCrawlerTask("https://1.1.1.1/legacy-owned-fixture")
			legacyRaw, err := legacy.ToString()
			require.NoError(t, err)
			_, err = server.Push(compiledLegacyCrawlerQueue, legacyRaw)
			require.NoError(t, err)
			target := "https://1.1.1.1/verified-owned-fixture"
			var id string
			switch producer {
			case "default":
				id, err = db.AddHTMLCrawlerTask(ctx, target)
			case "options":
				id, err = db.AddHTMLCrawlerTaskWithOptions(ctx, target, "", false)
			default:
				id, err = db.AddHTMLCrawlerTaskWithEgress(ctx, target, "", true, &CrawlerEgressPolicy{Host: "1.1.1.1", Addresses: []string{"1.1.1.1"}, MaxRedirects: 1})
			}
			require.NoError(t, err)
			pendingLegacy, err := server.List(compiledLegacyCrawlerQueue)
			require.NoError(t, err)
			require.Equal(t, []string{legacyRaw}, pendingLegacy, "a new producer must leave the entire legacy queue untouched")
			// A legacy consumer can remove only its original legacy task, then sees no
			// work even though the verified task is still pending for the new worker.
			poppedLegacy, err := db.GetDB().Client.LPop(ctx, compiledLegacyCrawlerQueue).Result()
			require.NoError(t, err)
			require.Equal(t, legacyRaw, poppedLegacy)
			require.ErrorIs(t, db.GetDB().Client.LPop(ctx, compiledLegacyCrawlerQueue).Err(), redis.Nil)
			task, err := db.GetHTMLCrawlerTask(ctx)
			require.NoError(t, err)
			require.Equal(t, id, task.TaskID)
			require.Equal(t, target, task.Url)
			require.NotNil(t, task.Egress)
			require.Empty(t, task.APIKey)
			task.Status = TaskStatusSuccess
			task.ResultHTML = []byte("<body>owned-verified-fixture</body>")
			task.ResultMarkdown = []byte("owned-verified-fixture")
			task.EgressReceipt = crawleregress.NewReceipt(task.TaskID, task.Url, *task.Egress)
			task.EgressReceipt.Origins = []crawleregress.Origin{{URL: task.Url, Addresses: task.Egress.Addresses, PeerAddress: "1.1.1.1", Kind: crawleregress.OriginDocument, BodyBytes: int64(len(task.ResultHTML))}}
			resultRaw, err := task.ToString()
			require.NoError(t, err)
			require.NoError(t, server.Set("laisky/tasks/html_crawler/result/"+id, resultRaw))
			result, err := db.GetHTMLCrawlerTaskResult(ctx, id)
			require.NoError(t, err)
			require.Equal(t, task, result)
			require.NoError(t, crawleregress.VerifyReceipt(ctx, id, target, *result.Egress, result.EgressReceipt))
		})
	}
}

// TestVerifiedCrawlerConsumerNeverFallsBackToLegacyQueue proves an empty
// verified queue does not consume, migrate or reinterpret existing old tasks.
func TestVerifiedCrawlerConsumerNeverFallsBackToLegacyQueue(t *testing.T) {
	server := miniredis.RunT(t)
	db := NewDB(&redis.Options{Addr: server.Addr(), Protocol: 2, MaxRetries: -1, DisableIdentity: true})
	t.Cleanup(func() { require.NoError(t, db.GetDB().Close()) })
	legacyRaw, err := NewHTMLCrawlerTask("https://1.1.1.1/legacy-only-fixture").ToString()
	require.NoError(t, err)
	_, err = server.Push(compiledLegacyCrawlerQueue, legacyRaw)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	task, err := db.GetHTMLCrawlerTask(ctx)
	require.Nil(t, task)
	require.True(t, errors.Is(err, context.DeadlineExceeded))
	pending, err := server.List(compiledLegacyCrawlerQueue)
	require.NoError(t, err)
	require.Equal(t, []string{legacyRaw}, pending)
}
