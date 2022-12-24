package web

import (
	"context"

	blog "github.com/Laisky/laisky-blog-graphql/internal/web/blog/controller"
	general "github.com/Laisky/laisky-blog-graphql/internal/web/general/controller"
	telegram "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/controller"
	twitter "github.com/Laisky/laisky-blog-graphql/internal/web/twitter/controller"
)

type Resolver struct{}

func (r *Resolver) Query() QueryResolver {
	return &queryResolver{}
}

func (r *Resolver) Mutation() MutationResolver {
	return &mutationResolver{}
}

// twitter

func (r *Resolver) Tweet() TweetResolver {
	return twitter.Instance.TweetResolver
}
func (r *Resolver) TwitterUser() TwitterUserResolver {
	return twitter.Instance.TwitterUserResolver
}

func (r *Resolver) EmbededTweet() EmbededTweetResolver {
	return twitter.Instance.EmbededTweetResolver
}

// blog

func (r *Resolver) BlogPost() BlogPostResolver {
	return blog.Instance.PostResolver
}
func (r *Resolver) BlogUser() BlogUserResolver {
	return blog.Instance.UserResolver
}
func (r *Resolver) BlogPostSeries() BlogPostSeriesResolver {
	return blog.Instance.PostSeriesResolver
}

// telegram

func (r *Resolver) TelegramAlertType() TelegramAlertTypeResolver {
	return telegram.Instance.TelegramAlertTypeResolver
}
func (r *Resolver) TelegramUser() TelegramUserResolver {
	return telegram.Instance.TelegramUserResolver
}

// general

func (r *Resolver) Lock() LockResolver {
	return general.Instance.LocksResolver
}

// =================
// query resolver
// =================

type twitterQuery struct {
	twitter.QueryResolver
}

type blogQuery struct {
	blog.QueryResolver
}

type telegramQuery struct {
	telegram.QueryResolver
}

type genaralQuery struct {
	general.QueryResolver
}

type queryResolver struct {
	twitterQuery
	blogQuery
	telegramQuery
	genaralQuery
}

func (r *queryResolver) Hello(ctx context.Context) (string, error) {
	return "hello, world", nil
}

// ============================
// mutations
// ============================

type blogMutation struct {
	blog.MutationResolver
}

type telegramMutation struct {
	telegram.MutationResolver
}

type generalMutation struct {
	general.MutationResolver
}

type mutationResolver struct {
	blogMutation
	telegramMutation
	generalMutation
}
