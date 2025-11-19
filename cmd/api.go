package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	gcmd "github.com/Laisky/go-utils/v6/cmd"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/cobra"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/web"
	blogCtl "github.com/Laisky/laisky-blog-graphql/internal/web/blog/controller"
	blogDao "github.com/Laisky/laisky-blog-graphql/internal/web/blog/dao"
	blogModel "github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	blogSvc "github.com/Laisky/laisky-blog-graphql/internal/web/blog/service"
	telegramCtl "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/controller"
	telegramDao "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dao"
	telegramModel "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	telegramSvc "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
	"github.com/Laisky/laisky-blog-graphql/library/db/arweave"
	"github.com/Laisky/laisky-blog-graphql/library/db/postgres"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/log"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
	"github.com/Laisky/laisky-blog-graphql/library/search/google"
	"github.com/Laisky/laisky-blog-graphql/library/search/serpgoogle"
)

var apiCMD = &cobra.Command{
	Use:   "api",
	Short: "api",
	Long:  `graphql API service for laisky`,
	Args:  gcmd.NoExtraArgs,
	PreRun: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		if err := initialize(ctx, cmd); err != nil {
			log.Logger.Panic("init", zap.Error(err))
		}
	},
	Run: func(cmd *cobra.Command, args []string) {
		if err := runAPI(); err != nil {
			log.Logger.Panic("run api", zap.Error(err))
		}
	},
}

func init() {
	rootCMD.AddCommand(apiCMD)
}

func runAPI() error {
	ctx := context.Background()
	logger := log.Logger.Named("api")

	arweave := arweave.NewArdrive(
		gconfig.S.GetString("settings.arweave.wallet_file"),
		gconfig.S.GetString("settings.arweave.folder_id"),
	)
	minioCli, err := minio.New(
		gconfig.S.GetString("settings.arweave.s3.endpoint"),
		&minio.Options{
			Creds: credentials.NewStaticV4(
				gconfig.S.GetString("settings.arweave.s3.access_key"),
				gconfig.S.GetString("settings.arweave.s3.secret"),
				"",
			),
			Secure: true,
		},
	)
	if err != nil {
		return errors.Wrap(err, "new minio client")
	}

	var args web.ResolverArgs

	// setup redis
	args.Rdb = rlibs.NewDB(&redis.Options{
		Addr: gconfig.S.GetString("settings.db.redis.addr"),
		DB:   gconfig.S.GetInt("settings.db.redis.db"),
	})

	{ // setup telegram
		monitorDB, err := telegramModel.NewMonitorDB(ctx)
		if err != nil {
			return errors.Wrap(err, "new monitor db")
		}
		telegramDB, err := telegramModel.NewTelegramDB(ctx)
		if err != nil {
			return errors.Wrap(err, "new telegram db")
		}

		var botToken = gconfig.Shared.GetString("settings.telegram.token")
		if os.Getenv("TELEGRAM_BOT_TOKEN") != "" {
			logger.Info("rewrite telegram bot token from env")
			botToken = os.Getenv("TELEGRAM_BOT_TOKEN")
		}

		args.TelegramSvc, err = telegramSvc.New(ctx,
			telegramDao.NewMonitor(monitorDB),
			telegramDao.NewTelegram(telegramDB),
			telegramDao.NewUpload(telegramDB, arweave, minioCli),
			botToken,
			gconfig.Shared.GetString("settings.telegram.api"),
		)
		if err != nil {
			logger.Error("new telegram service", zap.Error(err))
			// return errors.Wrap(err, "new telegram service")
		} else {
			args.TelegramCtl = telegramCtl.NewTelegram(ctx, args.TelegramSvc)
		}
	}

	{ // setup blog
		blogDB, err := blogModel.NewDB(ctx)
		if err != nil {
			return errors.Wrap(err, "new blog db")
		}
		blogDao := blogDao.New(logger.Named("blog_dao"), blogDB, arweave)
		args.BlogSvc, err = blogSvc.New(ctx, logger.Named("blog_svc"), blogDao)
		if err != nil {
			return errors.Wrap(err, "new blog service")
		}

		args.BlogCtl = blogCtl.New(args.BlogSvc)
	}

	{
		tiers, maxRetries := buildSearchEngineTiers(logger.Named("web_search_config"))
		if len(tiers) == 0 {
			logger.Warn("no web search engines configured")
		} else {
			manager, err := searchlib.NewManager(
				tiers,
				searchlib.WithLogger(log.Logger.Named("search_manager")),
				searchlib.WithMaxRetries(maxRetries),
			)
			if err != nil {
				logger.Error("init search manager", zap.Error(err))
			} else {
				args.WebSearchProvider = manager
			}
		}
	}

	var mcpDB *postgres.DB

	{ // setup ask_user service
		dial := postgres.DialInfo{
			Addr:   gconfig.S.GetString("settings.db.mcp.addr"),
			DBName: gconfig.S.GetString("settings.db.mcp.db"),
			User:   gconfig.S.GetString("settings.db.mcp.user"),
			Pwd:    gconfig.S.GetString("settings.db.mcp.pwd"),
		}
		if dial.Addr == "" || dial.DBName == "" || dial.User == "" {
			logger.Warn("ask_user database configuration incomplete",
				zap.Bool("addr_empty", dial.Addr == ""),
				zap.Bool("db_empty", dial.DBName == ""),
				zap.Bool("user_empty", dial.User == ""),
			)
		} else if askDB, err := postgres.NewDB(ctx, dial); err != nil {
			logger.Error("new ask_user postgres", zap.Error(err))
		} else {
			mcpDB = askDB
			if svc, err := askuser.NewService(askDB.DB, logger.Named("ask_user")); err != nil {
				logger.Error("init ask_user service", zap.Error(err))
			} else {
				args.AskUserService = svc
			}

			if callSvc, err := calllog.NewService(askDB.DB, logger.Named("call_log"), nil); err != nil {
				logger.Error("init call_log service", zap.Error(err))
			} else {
				args.CallLogService = callSvc
			}
		}
	}

	ragSettings := rag.LoadSettingsFromConfig()
	args.RAGSettings = ragSettings
	if ragSettings.Enabled {
		if mcpDB == nil {
			logger.Warn("extract_key_info dependencies missing", zap.Bool("mcp_db_nil", true))
		} else {
			embedder := rag.NewOpenAIEmbedder(ragSettings.OpenAIBaseURL, ragSettings.EmbeddingModel, nil)
			chunker := rag.ParagraphChunker{}
			if svc, err := rag.NewService(mcpDB.DB, embedder, chunker, ragSettings, logger.Named("extract_key_info")); err != nil {
				logger.Error("init extract_key_info service", zap.Error(err))
			} else {
				args.RAGService = svc
			}
		}
	}

	resolver := web.NewResolver(args)
	web.RunServer(gconfig.Shared.GetString("listen"), resolver)
	return nil
}

// configInt retrieves an integer configuration value using gconfig, falling back to def when missing or invalid.
func configInt(key string, def int) int {
	raw := gconfig.S.Get(key)
	switch value := raw.(type) {
	case nil:
		return def
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return def
		}
		if parsed, err := strconv.Atoi(trimmed); err == nil {
			return parsed
		}
	}
	return def
}

// buildSearchEngineTiers constructs the prioritized search-engine tiers and max retry setting from configuration.
func buildSearchEngineTiers(logger logSDK.Logger) ([][]searchlib.Engine, int) {
	maxRetries := configInt("settings.websearch.max_retry", 3)
	if maxRetries < 0 {
		maxRetries = 0
	}

	buckets := make(map[int][]searchlib.Engine)

	rawEngines := toStringMap(gconfig.S.Get("settings.websearch.engines"))
	for name, value := range rawEngines {
		cfg := toStringMap(value)
		if cfg == nil {
			logger.Warn("skip search engine with invalid config", zap.String("engine", name))
			continue
		}
		if !boolFromValue(cfg["enabled"], false) {
			continue
		}

		priority := intFromValue(cfg["priority"], 1)
		if priority < 1 {
			priority = 1
		}

		engine, err := instantiateSearchEngine(name, cfg)
		if err != nil {
			logger.Error("init search engine", zap.String("engine", name), zap.Error(err))
			continue
		}
		if engine == nil {
			logger.Warn("skip unknown search engine", zap.String("engine", name))
			continue
		}

		buckets[priority] = append(buckets[priority], engine)
	}

	if len(buckets) == 0 {
		addLegacySearchEngines(buckets, logger)
	}

	if len(buckets) == 0 {
		return nil, maxRetries
	}

	priorities := make([]int, 0, len(buckets))
	for priority := range buckets {
		priorities = append(priorities, priority)
	}
	sort.Ints(priorities)

	tiers := make([][]searchlib.Engine, 0, len(priorities))
	for _, priority := range priorities {
		tiers = append(tiers, buckets[priority])
	}

	return tiers, maxRetries
}

// instantiateSearchEngine creates a concrete search engine based on its name and raw configuration map.
func instantiateSearchEngine(name string, cfg map[string]any) (searchlib.Engine, error) {
	switch name {
	case "google":
		apiKey := stringFromValue(cfg["api_key"])
		cx := stringFromValue(cfg["cx"])
		if apiKey == "" || cx == "" {
			return nil, errors.New("missing api_key or cx")
		}
		adapter, err := searchlib.NewGoogleEngineAdapter(google.NewSearchEngine(apiKey, cx))
		if err != nil {
			return nil, errors.Wrap(err, "wrap google adapter")
		}
		return adapter, nil
	case "serp_google":
		apiKey := stringFromValue(cfg["api_key"])
		if apiKey == "" {
			return nil, errors.New("missing api_key")
		}
		return serpgoogle.NewSearchEngine(apiKey), nil
	default:
		return nil, nil
	}
}

// addLegacySearchEngines populates engines using the legacy flat configuration keys when the new format is absent.
func addLegacySearchEngines(buckets map[int][]searchlib.Engine, logger logSDK.Logger) {
	apiKey := strings.TrimSpace(gconfig.S.GetString("settings.websearch.google.api_key"))
	cx := strings.TrimSpace(gconfig.S.GetString("settings.websearch.google.cx"))
	if apiKey != "" && cx != "" {
		adapter, err := searchlib.NewGoogleEngineAdapter(google.NewSearchEngine(apiKey, cx))
		if err != nil {
			logger.Error("init legacy google search adapter", zap.Error(err))
		} else {
			buckets[1] = append(buckets[1], adapter)
		}
	}

	serpKey := strings.TrimSpace(gconfig.S.GetString("settings.websearch.serp_google.api_key"))
	if serpKey != "" {
		buckets[2] = append(buckets[2], serpgoogle.NewSearchEngine(serpKey))
	}
}

// boolFromValue attempts to coerce the provided value into a boolean, returning def when conversion fails.
func boolFromValue(value any, def bool) bool {
	switch v := value.(type) {
	case nil:
		return def
	case bool:
		return v
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return def
		}
		if parsed, err := strconv.ParseBool(trimmed); err == nil {
			return parsed
		}
		if parsed, err := strconv.Atoi(trimmed); err == nil {
			return parsed != 0
		}
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	}
	return def
}

// intFromValue attempts to coerce the provided value into an integer, returning def when conversion fails.
func intFromValue(value any, def int) int {
	switch v := value.(type) {
	case nil:
		return def
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return def
		}
		if parsed, err := strconv.Atoi(trimmed); err == nil {
			return parsed
		}
	}
	return def
}

// stringFromValue coerces the provided value into a trimmed string representation.
func stringFromValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// toStringMap converts supported map structures into map[string]any for uniform processing.
func toStringMap(value any) map[string]any {
	switch v := value.(type) {
	case map[string]any:
		return v
	case map[interface{}]interface{}:
		result := make(map[string]any, len(v))
		for key, item := range v {
			result[fmt.Sprint(key)] = item
		}
		return result
	default:
		return nil
	}
}
