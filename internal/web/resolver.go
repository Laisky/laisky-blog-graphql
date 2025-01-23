package web

import (
	"context"

	arweave "github.com/Laisky/laisky-blog-graphql/internal/web/arweave/controller"
	blog "github.com/Laisky/laisky-blog-graphql/internal/web/blog/controller"
	blogSvc "github.com/Laisky/laisky-blog-graphql/internal/web/blog/service"
	general "github.com/Laisky/laisky-blog-graphql/internal/web/general/controller"
	telegram "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/controller"
	telegramSvc "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
	twitter "github.com/Laisky/laisky-blog-graphql/internal/web/twitter/controller"
)

// Resolver resolver
type Resolver struct {
	telegramCtl *telegram.Telegram
	telegramSvc telegramSvc.Interface
	blogCtl     *blog.Blog
	blogSvc     *blogSvc.Blog
}

type ResolverArgs struct {
	TelegramCtl *telegram.Telegram
	TelegramSvc telegramSvc.Interface
	BlogCtl     *blog.Blog
	BlogSvc     *blogSvc.Blog
}

// NewResolver new resolver
func NewResolver(arg ResolverArgs) *Resolver {
	return &Resolver{
		telegramCtl: arg.TelegramCtl,
		telegramSvc: arg.TelegramSvc,
		blogCtl:     arg.BlogCtl,
		blogSvc:     arg.BlogSvc,
	}
}

// Query query resolver
func (r *Resolver) Query() QueryResolver {
	return &queryResolver{
		telegramQuery: telegramQuery{
			QueryResolver: telegram.NewQueryResolver(r.telegramSvc),
		},
		blogQuery: blogQuery{
			QueryResolver: blog.NewQueryResolver(r.blogSvc),
		},
	}
}

// Mutation mutation resolver
func (r *Resolver) Mutation() MutationResolver {
	return &mutationResolver{
		blogMutation: blogMutation{
			MutationResolver: blog.NewMutationResolver(r.blogSvc),
		},
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
	return r.blogCtl.PostResolver
}
func (r *Resolver) BlogUser() BlogUserResolver {
	return r.blogCtl.UserResolver
}
func (r *Resolver) BlogPostSeries() BlogPostSeriesResolver {
	return r.blogCtl.PostSeriesResolver
}
func (r *Resolver) ArweaveItem() ArweaveItemResolver {
	return r.blogCtl.ArweaveItemResolver
}

// telegram

func (r *Resolver) TelegramAlertType() TelegramAlertTypeResolver {
	return r.telegramCtl.TelegramAlertTypeResolver
}
func (r *Resolver) TelegramMonitorUser() TelegramMonitorUserResolver {
	return r.telegramCtl.TelegramMonitorUserResolver
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
	arweave.MutationResolver
}
