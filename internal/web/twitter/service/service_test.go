// Package service service for twitter API
package service

import (
	"context"
	"os"
	"testing"

	"github.com/Laisky/laisky-blog-graphql/internal/web/twitter/dto"
	"github.com/Laisky/laisky-blog-graphql/library/config"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/stretchr/testify/require"
)

func TestType_LoadTweets(t *testing.T) {
	if os.Getenv("RUN_TWITTER_INTEGRATION_TESTS") == "" {
		t.Skip("integration test requires RUN_TWITTER_INTEGRATION_TESTS=1 and a reachable MongoDB instance")
	}

	ctx := context.Background()
	config.LoadTest()
	Initialize(ctx)

	err := log.Logger.ChangeLevel(glog.LevelDebug)
	require.NoError(t, err)

	ts, err := Instance.LoadTweets(ctx, &dto.LoadTweetArgs{
		Regexp: "饥荒",
	})
	require.NoError(t, err)
	t.Log(ts)
}
