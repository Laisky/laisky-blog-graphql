package web

import (
	"context"

	blog "github.com/Laisky/laisky-blog-graphql/internal/web/blog/controller"
	general "github.com/Laisky/laisky-blog-graphql/internal/web/general/controller"
	telegram "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/controller"
	telegramSvc "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
	twitter "github.com/Laisky/laisky-blog-graphql/internal/web/twitter/controller"
)

type Resolver struct {
	telegramController *telegram.Telegram
	telegramSvc        telegramSvc.Interface
}

func NewResolver(
	telegramController *telegram.Telegram,
	telegramSvc telegramSvc.Interface,
) *Resolver {
	return &Resolver{
		telegramController: telegramController,
		telegramSvc:        telegramSvc,
	}
}

func (r *Resolver) Query() QueryResolver {
	return &queryResolver{}
}

func (r *Resolver) Mutation() MutationResolver {
	return &mutationResolver{
		blogMutation: blogMutation{},
		telegramMutation: telegramMutation{
			MutationResolver: telegram.NewMutationResolver(r.telegramSvc),
		},
		generalMutation: generalMutation{},
	}
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
	return r.telegramController.TelegramAlertTypeResolver
}
func (r *Resolver) TelegramUser() TelegramUserResolver {
	return r.telegramController.TelegramUserResolver
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
