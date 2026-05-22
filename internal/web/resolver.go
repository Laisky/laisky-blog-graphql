package web

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/internal/library/search"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	mcptools "github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
	arweave "github.com/Laisky/laisky-blog-graphql/internal/web/arweave/controller"
	blog "github.com/Laisky/laisky-blog-graphql/internal/web/blog/controller"
	blogSvc "github.com/Laisky/laisky-blog-graphql/internal/web/blog/service"
	general "github.com/Laisky/laisky-blog-graphql/internal/web/general/controller"
	telegram "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/controller"
	telegramDao "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dao"
	telegramSvc "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
	twitter "github.com/Laisky/laisky-blog-graphql/internal/web/twitter/controller"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
)

// Resolver resolver
type Resolver struct {
	args ResolverArgs

	queryResolver    *queryResolver
	mutationResolver *mutationResolver
	telegramCtl      *telegram.Telegram
}

type ResolverArgs struct {
	TelegramCtl        *telegram.Telegram
	TelegramSvc        *telegramSvc.Telegram
	BlogCtl            *blog.Blog
	BlogSvc            *blogSvc.Blog
	WebSearchProvider  searchlib.Provider
	Rdb                *rlibs.DB
	AskUserService     *askuser.Service
	CallLogService     *calllog.Service
	UserRequestService *userrequests.Service
	UserRequestImages  *userrequests.ImageManager
	FilesService       *files.Service
	MCPFileService     mcptools.FileService
	MemoryService      *mcpmemory.Service
	RAGService         *rag.Service
	RAGSettings        rag.Settings
	MCPToolsSettings   mcp.ToolsSettings
}

// NewResolver new resolver. All sub-resolvers are eagerly built once so that
// Query()/Mutation() are cheap and a missing optional dependency (e.g. the
// Telegram service failed to start) surfaces at startup instead of panicking
// inside per-request resolver dispatch.
func NewResolver(args ResolverArgs) *Resolver {
	if args.Rdb != nil {
		general.ConfigureTaskStore(args.Rdb)
	}
	r := &Resolver{args: args}
	// Fall back to a stub Telegram controller (with a nil service) when
	// telegram init failed, so the TelegramAlertType/TelegramMonitorUser
	// resolver-getters do not nil-deref. The underlying resolver methods
	// guard against a nil service and return errors instead of panicking.
	r.telegramCtl = args.TelegramCtl
	if r.telegramCtl == nil {
		r.telegramCtl = telegram.NewTelegram(context.Background(), nil)
	}
	r.queryResolver = r.buildQueryResolver()
	r.mutationResolver = r.buildMutationResolver()
	return r
}

func (r *Resolver) buildQueryResolver() *queryResolver {
	return &queryResolver{
		twitterQuery: twitterQuery{
			QueryResolver: twitter.QueryResolver{},
		},
		telegramQuery: telegramQuery{
			QueryResolver: telegram.NewQueryResolver(r.args.TelegramSvc),
		},
		blogQuery: blogQuery{
			QueryResolver: blog.NewQueryResolver(r.args.BlogSvc),
		},
		genaralQuery: genaralQuery{
			QueryResolver: general.QueryResolver{},
		},
	}
}

func (r *Resolver) buildMutationResolver() *mutationResolver {
	// Arweave uploads ride on the Telegram service's UploadDao, so the
	// arweave mutation resolver can only be wired when TelegramSvc is
	// available. A nil dao here is fine: the resolver method guards it
	// and returns a clean error instead of panicking.
	var uploadDao *telegramDao.Upload
	if r.args.TelegramSvc != nil {
		uploadDao = r.args.TelegramSvc.UploadDao
	}
	return &mutationResolver{
		blogMutation: blogMutation{
			MutationResolver: blog.NewMutationResolver(r.args.BlogSvc),
		},
		telegramMutation: telegramMutation{
			MutationResolver: telegram.NewMutationResolver(r.args.TelegramSvc),
		},
		generalMutation: generalMutation{
			MutationResolver: &general.MutationResolver{},
		},
		arweaveMutation: arweaveMutation{
			MutationResolver: arweave.NewMutationResolver(uploadDao),
		},
		webSearchMutation: webSearchMutation{
			MutationResolver: search.NewMutationResolver(
				r.args.WebSearchProvider,
				r.args.Rdb,
				r.args.CallLogService,
			),
		},
	}
}

// Query query resolver
func (r *Resolver) Query() QueryResolver { return r.queryResolver }

// Mutation mutation resolver
func (r *Resolver) Mutation() MutationResolver { return r.mutationResolver }

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
