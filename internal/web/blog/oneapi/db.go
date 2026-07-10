// Package oneapi provides SSO access to the authoritative OneAPI user
// database while preserving the blog's public UUID and ObjectID contracts.
package oneapi

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const (
	defaultSQLiteBusyTimeoutMS = 10_000
	defaultMaxIdleConns        = 5
	defaultMaxOpenConns        = 20
	defaultConnMaxLifetime     = 300 * time.Second
)

// Options configures a connection to the database used by OneAPI.
type Options struct {
	Driver            string
	DSN               string
	SQLitePath        string
	SQLiteBusyTimeout int
	MaxIdleConns      int
	MaxOpenConns      int
	ConnMaxLifetime   time.Duration
}

// NewDB opens and verifies a PostgreSQL, MySQL, or SQLite connection without
// logging its sensitive DSN.
func NewDB(ctx context.Context, opts Options) (*gorm.DB, error) {
	driver := strings.ToLower(strings.TrimSpace(opts.Driver))
	gormConfig := &gorm.Config{
		PrepareStmt:    true,
		TranslateError: true,
		Logger:         gormlogger.Default.LogMode(gormlogger.Silent),
	}

	var (
		db       *gorm.DB
		err      error
		isSQLite bool
	)
	switch driver {
	case "postgres", "postgresql":
		dsn := strings.TrimSpace(opts.DSN)
		if dsn == "" {
			return nil, errors.New("oneapi postgres dsn is empty")
		}
		db, err = gorm.Open(postgres.New(postgres.Config{
			DSN:                  dsn,
			PreferSimpleProtocol: true,
		}), gormConfig)
	case "mysql":
		dsn, normalizeErr := normalizeMySQLDSN(strings.TrimSpace(opts.DSN))
		if normalizeErr != nil {
			return nil, errors.Wrap(normalizeErr, "normalize oneapi mysql dsn")
		}
		db, err = gorm.Open(mysql.Open(dsn), gormConfig)
	case "sqlite":
		isSQLite = true
		path := strings.TrimSpace(opts.SQLitePath)
		if path == "" {
			return nil, errors.New("oneapi sqlite path is empty")
		}
		busyTimeout := opts.SQLiteBusyTimeout
		if busyTimeout <= 0 {
			busyTimeout = defaultSQLiteBusyTimeoutMS
		}
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		dsn := fmt.Sprintf("%s%s_busy_timeout=%d&_journal_mode=WAL&_synchronous=NORMAL", path, separator, busyTimeout)
		db, err = gorm.Open(sqlite.Open(dsn), gormConfig)
	default:
		return nil, errors.Errorf("unsupported oneapi database driver %q", driver)
	}
	if err != nil {
		return nil, errors.Wrap(err, "open oneapi database")
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, errors.Wrap(err, "get oneapi sql database")
	}
	if isSQLite {
		sqlDB.SetMaxIdleConns(1)
		sqlDB.SetMaxOpenConns(1)
	} else {
		maxIdle := opts.MaxIdleConns
		if maxIdle <= 0 {
			maxIdle = defaultMaxIdleConns
		}
		maxOpen := opts.MaxOpenConns
		if maxOpen <= 0 {
			maxOpen = defaultMaxOpenConns
		}
		if maxIdle > maxOpen {
			_ = sqlDB.Close()
			return nil, errors.New("oneapi max idle connections exceeds max open connections")
		}
		sqlDB.SetMaxIdleConns(maxIdle)
		sqlDB.SetMaxOpenConns(maxOpen)
	}
	lifetime := opts.ConnMaxLifetime
	if lifetime <= 0 {
		lifetime = defaultConnMaxLifetime
	}
	sqlDB.SetConnMaxLifetime(lifetime)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err = sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, errors.Wrap(err, "ping oneapi database")
	}
	return db, nil
}

func normalizeMySQLDSN(dsn string) (string, error) {
	if dsn == "" {
		return "", errors.New("oneapi mysql dsn is empty")
	}
	if strings.HasPrefix(strings.ToLower(dsn), "mysql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			return "", errors.Wrap(err, "parse mysql url")
		}
		if parsed.Host == "" {
			return "", errors.New("mysql dsn is missing host")
		}
		userInfo := ""
		if parsed.User != nil {
			userInfo = parsed.User.Username()
			if password, ok := parsed.User.Password(); ok {
				userInfo += ":" + password
			}
		}
		if userInfo != "" {
			userInfo += "@"
		}
		dsn = fmt.Sprintf("%stcp(%s)/%s", userInfo, parsed.Host, strings.TrimPrefix(parsed.Path, "/"))
		if parsed.RawQuery != "" {
			dsn += "?" + parsed.RawQuery
		}
	}

	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return "", errors.Wrap(err, "parse mysql dsn")
	}
	config.ParseTime = true
	if !hasDSNQueryKey(dsn, "loc") {
		config.Loc = time.UTC
	}
	return config.FormatDSN(), nil
}

func hasDSNQueryKey(dsn string, key string) bool {
	queryOffset := strings.IndexByte(dsn, '?')
	if queryOffset < 0 {
		return false
	}
	values, err := url.ParseQuery(dsn[queryOffset+1:])
	if err != nil {
		return false
	}
	_, ok := values[key]
	return ok
}
