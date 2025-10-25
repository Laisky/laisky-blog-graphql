package web

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/internal/library/search"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	arweave "github.com/Laisky/laisky-blog-graphql/internal/web/arweave/controller"
	blog "github.com/Laisky/laisky-blog-graphql/internal/web/blog/controller"
	blogSvc "github.com/Laisky/laisky-blog-graphql/internal/web/blog/service"
	general "github.com/Laisky/laisky-blog-graphql/internal/web/general/controller"
	telegram "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/controller"
	telegramSvc "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
	twitter "github.com/Laisky/laisky-blog-graphql/internal/web/twitter/controller"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/search/google"
)

// Resolver resolver
type Resolver struct {
	args ResolverArgs
}

type ResolverArgs struct {
	TelegramCtl     *telegram.Telegram
	TelegramSvc     *telegramSvc.Telegram
	BlogCtl         *blog.Blog
	BlogSvc         *blogSvc.Blog
	WebSearchEngine *google.SearchEngine
	Rdb             *rlibs.DB
	AskUserService  *askuser.Service
}

// NewResolver new resolver
func NewResolver(args ResolverArgs) *Resolver {
	return &Resolver{
		args: args,
	}
}

// Query query resolver
func (r *Resolver) Query() QueryResolver {
	return &queryResolver{
		telegramQuery: telegramQuery{
			QueryResolver: telegram.NewQueryResolver(r.args.TelegramSvc),
		},
		blogQuery: blogQuery{
			QueryResolver: blog.NewQueryResolver(r.args.BlogSvc),
		},
	}
}

// Mutation mutation resolver
func (r *Resolver) Mutation() MutationResolver {
	return &mutationResolver{
		blogMutation: blogMutation{
			MutationResolver: blog.NewMutationResolver(r.args.BlogSvc),
		},
		telegramMutation: telegramMutation{
			MutationResolver: telegram.NewMutationResolver(r.args.TelegramSvc),
		},
		generalMutation: generalMutation{},
		arweaveMutation: arweaveMutation{
			MutationResolver: arweave.NewMutationResolver(r.args.TelegramSvc.UploadDao),
		},
		webSearchMutation: webSearchMutation{
			MutationResolver: search.NewMutationResolver(
				r.args.WebSearchEngine,
				r.args.Rdb,
			),
		},
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
	return r.args.BlogCtl.PostResolver
}
func (r *Resolver) BlogUser() BlogUserResolver {
	return r.args.BlogCtl.UserResolver
}
func (r *Resolver) BlogPostSeries() BlogPostSeriesResolver {
	return r.args.BlogCtl.PostSeriesResolver
}
func (r *Resolver) ArweaveItem() ArweaveItemResolver {
	return r.args.BlogCtl.ArweaveItemResolver
}

// web search

func (r *Resolver) WebSearchResult() WebSearchResultResolver {
	return new(search.WebSearchResultResolver)
}

// telegram

func (r *Resolver) TelegramAlertType() TelegramAlertTypeResolver {
	return r.args.TelegramCtl.TelegramAlertTypeResolver
}
func (r *Resolver) TelegramMonitorUser() TelegramMonitorUserResolver {
	return r.args.TelegramCtl.TelegramMonitorUserResolver
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
	*blog.MutationResolver
}

type telegramMutation struct {
	*telegram.MutationResolver
}

type generalMutation struct {
	*general.MutationResolver
}

type arweaveMutation struct {
	*arweave.MutationResolver
}

type webSearchMutation struct {
	*search.MutationResolver
}

type mutationResolver struct {
	blogMutation
	telegramMutation
	generalMutation
	arweaveMutation
	webSearchMutation
}
