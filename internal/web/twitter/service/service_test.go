// Package service service for twitter API
package service

import (
	"context"
	"testing"

	"laisky-blog-graphql/internal/web/twitter/dto"
	"laisky-blog-graphql/library/config"
	"laisky-blog-graphql/library/log"

	glog "github.com/Laisky/go-utils/v2/log"
	"github.com/stretchr/testify/require"
)

func TestType_LoadTweets(t *testing.T) {
	ctx := context.Background()
	config.LoadTest()
	Initialize(ctx)

	err := log.Logger.ChangeLevel(glog.LevelDebug)
	require.NoError(t, err)

	ts, err := Instance.LoadTweets(&dto.LoadTweetArgs{
		Regexp: "饥荒",
	})
	require.NoError(t, err)
	t.Log(ts)
}
