package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	gcmd "github.com/Laisky/go-utils/v6/cmd"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
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
	mongodb "github.com/Laisky/laisky-blog-graphql/library/db/mongo"
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
	startTime := time.Now()
	if err := validateStartupConfig(); err != nil {
		return errors.Wrap(err, "validate startup configuration")
	}

	arweaveClient := arweave.NewArdrive(
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

	// setup redis (non-blocking, lazy connection)
	args.Rdb = rlibs.NewDB(&redis.Options{
		Addr: gconfig.S.GetString("settings.db.redis.addr"),
		DB:   gconfig.S.GetInt("settings.db.redis.db"),
	})

	// ============================================================
	// Parallel database initialization using errgroup
	// ============================================================
	var (
		monitorDB  mongodb.DB
		telegramDB mongodb.DB
		blogDB     mongodb.DB
		mcpDB      *postgres.DB
		mcpPGXPool *pgxpool.Pool
		dbMutex    sync.Mutex
	)

	logger.Debug("starting parallel database initialization")
	dbInitStart := time.Now()

	eg, egCtx := errgroup.WithContext(ctx)

	// MongoDB: monitor
	eg.Go(func() error {
		db, err := telegramModel.NewMonitorDB(egCtx)
		if err != nil {
			return errors.Wrap(err, "new monitor db")
		}
		dbMutex.Lock()
		monitorDB = db
		dbMutex.Unlock()
		return nil
	})

	// MongoDB: telegram
	eg.Go(func() error {
		db, err := telegramModel.NewTelegramDB(egCtx)
		if err != nil {
			return errors.Wrap(err, "new telegram db")
		}
		dbMutex.Lock()
		telegramDB = db
		dbMutex.Unlock()
		return nil
	})

	// MongoDB: blog
	eg.Go(func() error {
		db, err := blogModel.NewDB(egCtx)
		if err != nil {
			return errors.Wrap(err, "new blog db")
		}
		dbMutex.Lock()
		blogDB = db
		dbMutex.Unlock()
		return nil
	})

	// PostgreSQL: MCP database
	eg.Go(func() error {
		dial := postgres.DialInfo{
			Addr:   gconfig.S.GetString("settings.db.mcp.addr"),
			DBName: gconfig.S.GetString("settings.db.mcp.db"),
			User:   gconfig.S.GetString("settings.db.mcp.user"),
			Pwd:    gconfig.S.GetString("settings.db.mcp.pwd"),
		}
		if dial.Addr == "" || dial.DBName == "" || dial.User == "" {
			logger.Warn("mcp database configuration incomplete",
				zap.Bool("addr_empty", dial.Addr == ""),
				zap.Bool("db_empty", dial.DBName == ""),
				zap.Bool("user_empty", dial.User == ""),
			)
			return nil // Not a critical error
		}
		db, err := postgres.NewDB(egCtx, dial)
		if err != nil {
			logger.Error("new mcp postgres", zap.Error(err))
			return nil // Log but don't fail startup
		}
		pgxPool, pgxErr := pgxpool.New(egCtx, postgres.BuildDSN(dial))
		if pgxErr != nil {
			logger.Warn("new mcp pgx pool", zap.Error(pgxErr))
		}
		dbMutex.Lock()
		mcpDB = db
		mcpPGXPool = pgxPool
		dbMutex.Unlock()
		return nil
	})

	// Wait for all database connections
	if err := eg.Wait(); err != nil {
		return errors.Wrap(err, "parallel database initialization failed")
	}
	logger.Info("parallel database initialization completed",
		zap.Duration("duration", time.Since(dbInitStart)))

	// ============================================================
	// Service initialization (depends on database connections)
	// ============================================================
	serviceInitStart := time.Now()

	// Setup telegram service
	telegramStart := time.Now()
	if monitorDB != nil && telegramDB != nil {
		var botToken = gconfig.Shared.GetString("settings.telegram.token")
		if os.Getenv("TELEGRAM_BOT_TOKEN") != "" {
			logger.Info("rewrite telegram bot token from env")
			botToken = os.Getenv("TELEGRAM_BOT_TOKEN")
		}

		args.TelegramSvc, err = telegramSvc.New(ctx,
			telegramDao.NewMonitor(monitorDB),
			telegramDao.NewTelegram(telegramDB),
			telegramDao.NewUpload(telegramDB, arweaveClient, minioCli),
			telegramDao.NewAskUserToken(monitorDB),
			botToken,
			gconfig.Shared.GetString("settings.telegram.api"),
		)
		if err != nil {
			logger.Error("new telegram service", zap.Error(err))
		} else {
			args.TelegramCtl = telegramCtl.NewTelegram(ctx, args.TelegramSvc)
		}
	} else {
		logger.Warn("telegram service skipped due to missing database connections",
			zap.Bool("monitor_nil", monitorDB == nil),
			zap.Bool("telegram_nil", telegramDB == nil))
	}
	logger.Debug("telegram service initialization completed",
		zap.Duration("duration", time.Since(telegramStart)))

	// Setup blog service
	blogStart := time.Now()
	if blogDB != nil {
		blogDaoInst := blogDao.New(logger.Named("blog_dao"), blogDB, arweaveClient)
		args.BlogSvc, err = blogSvc.New(ctx, logger.Named("blog_svc"), blogDaoInst)
		if err != nil {
			return errors.Wrap(err, "new blog service")
		}
		args.BlogCtl = blogCtl.New(args.BlogSvc)
	} else {
		logger.Warn("blog service skipped due to missing database connection")
	}
	logger.Debug("blog service initialization completed",
		zap.Duration("duration", time.Since(blogStart)))

	// Setup search engines
	searchStart := time.Now()
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
	logger.Debug("search engines initialization completed",
		zap.Duration("duration", time.Since(searchStart)))

	// Setup MCP services (ask_user, call_log, user_requests, RAG)
	mcpStart := time.Now()
	ragSettings := rag.LoadSettingsFromConfig()
	args.RAGSettings = ragSettings
	if mcpDB != nil {
		var (
			askSvc  *askuser.Service
			callSvc *calllog.Service
			userSvc *userrequests.Service
			ragSvc  *rag.Service
			svcMu   sync.Mutex
		)

		egServices, _ := errgroup.WithContext(ctx)

		egServices.Go(func() error {
			start := time.Now()
			svc, err := askuser.NewService(mcpDB.DB, logger.Named("ask_user"))
			if err != nil {
				logger.Error("init ask_user service", zap.Error(err))
				return nil
			}
			svcMu.Lock()
			askSvc = svc
			svcMu.Unlock()
			logger.Debug("ask_user service initialization completed",
				zap.Duration("duration", time.Since(start)))
			return nil
		})

		egServices.Go(func() error {
			start := time.Now()
			if mcpPGXPool == nil {
				logger.Warn("skip call_log service initialization because pgx pool is unavailable")
				return nil
			}
			svc, err := calllog.NewService(mcpPGXPool, logger.Named("call_log"), nil)
			if err != nil {
				logger.Error("init call_log service", zap.Error(err))
				return nil
			}
			svcMu.Lock()
			callSvc = svc
			svcMu.Unlock()
			logger.Debug("call_log service initialization completed",
				zap.Duration("duration", time.Since(start)))
			return nil
		})

		egServices.Go(func() error {
			start := time.Now()
			svc, err := userrequests.NewService(mcpDB.DB, logger.Named("user_requests"), nil, userrequests.LoadSettingsFromConfig())
			if err != nil {
				logger.Error("init user_requests service", zap.Error(err))
				return nil
			}
			svc.StartRetentionWorker(ctx)
			svcMu.Lock()
			userSvc = svc
			svcMu.Unlock()
			logger.Debug("user_requests service initialization completed",
				zap.Duration("duration", time.Since(start)))
			return nil
		})

		if ragSettings.Enabled {
			embedder := rag.NewOpenAIEmbedder(ragSettings.OpenAIBaseURL, ragSettings.EmbeddingModel, nil)
			chunker := rag.ParagraphChunker{}
			egServices.Go(func() error {
				start := time.Now()
				svc, err := rag.NewService(mcpDB.DB, embedder, chunker, ragSettings, logger.Named("extract_key_info"))
				if err != nil {
					logger.Error("init extract_key_info service", zap.Error(err))
					return nil
				}
				svcMu.Lock()
				ragSvc = svc
				svcMu.Unlock()
				logger.Debug("rag service initialization completed",
					zap.Duration("duration", time.Since(start)))
				return nil
			})
		}

		if err := egServices.Wait(); err != nil {
			return errors.Wrap(err, "initialize MCP services")
		}

		if askSvc != nil {
			args.AskUserService = askSvc
			if args.TelegramSvc != nil {
				args.TelegramSvc.SetAskUserService(askSvc)
			}
		} else {
			logger.Warn("ask_user service unavailable")
		}

		if callSvc != nil {
			args.CallLogService = callSvc
		} else {
			logger.Warn("call_log service unavailable")
		}

		if userSvc != nil {
			args.UserRequestService = userSvc
		} else {
			logger.Warn("user_requests service unavailable")
		}

		if ragSettings.Enabled {
			if ragSvc != nil {
				args.RAGService = ragSvc
			} else {
				logger.Warn("rag service unavailable despite being enabled")
			}
		}

		filesSettings := files.LoadSettingsFromConfig()
		if mcpDB != nil {
			memorySettings := mcpmemory.LoadSettingsFromConfig()
			var credStore files.CredentialStore
			if args.Rdb != nil {
				credStore = files.NewRedisCredentialStore(args.Rdb.GetDB())
			}
			credential, credErr := buildFileCredentialProtector(filesSettings)
			if credErr != nil {
				return errors.Wrap(credErr, "invalid mcp files credential configuration")
			}
			embedder := rag.NewOpenAIEmbedder(filesSettings.EmbeddingBaseURL, filesSettings.EmbeddingModel, nil)
			rerankClient := files.NewCohereRerankClient(filesSettings.Search.RerankEndpoint, filesSettings.Search.RerankModel, filesSettings.Search.RerankTimeout)
			fileSvc, err := files.NewService(mcpDB.DB, filesSettings, embedder, rerankClient, credential, credStore, logger.Named("mcp_files"), nil, nil)
			if err != nil {
				logger.Warn("file service unavailable", zap.Error(err))
			} else {
				args.FilesService = fileSvc
				if startErr := fileSvc.StartIndexWorkers(ctx); startErr != nil {
					logger.Warn("start file index workers", zap.Error(startErr))
				}

				memorySvc, memoryErr := mcpmemory.NewService(mcpDB.DB, fileSvc, memorySettings, logger.Named("mcp_memory"), nil)
				if memoryErr != nil {
					logger.Warn("memory service unavailable", zap.Error(memoryErr))
				} else {
					args.MemoryService = memorySvc
				}
			}
		}
	} else {
		logger.Warn("MCP services skipped due to missing database connection")
	}
	logger.Debug("mcp services initialization completed",
		zap.Duration("duration", time.Since(mcpStart)))

	logger.Info("service initialization completed",
		zap.Duration("duration", time.Since(serviceInitStart)))

	// Load MCP tools settings to control which tools are enabled
	args.MCPToolsSettings = mcp.LoadToolsSettingsFromConfig()
	logger.Info("loaded MCP tools settings",
		zap.Bool("web_search_enabled", args.MCPToolsSettings.WebSearchEnabled),
		zap.Bool("web_fetch_enabled", args.MCPToolsSettings.WebFetchEnabled),
		zap.Bool("ask_user_enabled", args.MCPToolsSettings.AskUserEnabled),
		zap.Bool("get_user_request_enabled", args.MCPToolsSettings.GetUserRequestEnabled),
		zap.Bool("extract_key_info_enabled", args.MCPToolsSettings.ExtractKeyInfoEnabled),
		zap.Bool("file_io_enabled", args.MCPToolsSettings.FileIOEnabled),
		zap.Bool("memory_enabled", args.MCPToolsSettings.MemoryEnabled),
	)

	logger.Info("total startup initialization completed",
		zap.Duration("total_duration", time.Since(startTime)))

	resolver := web.NewResolver(args)
	web.RunServer(gconfig.Shared.GetString("listen"), resolver)
	return nil
}

// buildFileCredentialProtector validates and constructs the FileIO credential protector.
// It accepts FileIO settings and returns nil when encryption is not configured, or a protector/error when configured.
func buildFileCredentialProtector(settings files.Settings) (*files.CredentialProtector, error) {
	if len(settings.Security.KEKs()) == 0 {
		return nil, nil
	}

	credential, err := files.NewCredentialProtector(settings.Security)
	if err != nil {
		return nil, errors.Wrap(err, "new file credential protector")
	}

	return credential, nil
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
