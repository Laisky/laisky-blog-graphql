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

// minPrepareStmtPoolSize is the smallest connection pool that may also cache
// prepared statements.
//
// GORM holds its prepared-statement cache mutex while database/sql prepares a
// statement, and that preparation needs a pooled connection. A goroutine
// already inside a transaction owns a connection and then needs the same mutex,
// so with too few connections the two resources deadlock permanently
// (go-gorm/gorm#7350 and #7465, both still open as of 2026; GORM's own docs say
// nothing about the pool interaction). A single-connection pool can always
// reach the cycle, so the cache is disabled there. For the multi-connection
// drivers the pool must stay larger than the peak number of concurrent
// in-transaction requests, which is why lowering MaxOpenConns to the floor also
// turns the cache off rather than trading a hang for throughput.
const minPrepareStmtPoolSize = 2

// poolSizeFor resolves the effective pool bounds before the connection opens,
// so the prepared-statement decision is made from the pool that will exist.
func poolSizeFor(isSQLite bool, opts Options) (maxIdle, maxOpen int, err error) {
	if isSQLite {
		// SQLite is single-writer; one connection also keeps WAL access serial.
		return 1, 1, nil
	}
	maxIdle, maxOpen = opts.MaxIdleConns, opts.MaxOpenConns
	if maxIdle <= 0 {
		maxIdle = defaultMaxIdleConns
	}
	if maxOpen <= 0 {
		maxOpen = defaultMaxOpenConns
	}
	if maxIdle > maxOpen {
		return 0, 0, errors.New("oneapi max idle connections exceeds max open connections")
	}
	return maxIdle, maxOpen, nil
}

// NewDB opens and verifies a PostgreSQL, MySQL, or SQLite connection without
// logging its sensitive DSN.
func NewDB(ctx context.Context, opts Options) (*gorm.DB, error) {
	driver := strings.ToLower(strings.TrimSpace(opts.Driver))
	isSQLiteDriver := driver == "sqlite"
	maxIdleConns, maxOpenConns, err := poolSizeFor(isSQLiteDriver, opts)
	if err != nil {
		return nil, err
	}
	gormConfig := &gorm.Config{
		PrepareStmt:    maxOpenConns >= minPrepareStmtPoolSize,
		TranslateError: true,
		Logger:         gormlogger.Default.LogMode(gormlogger.Silent),
	}

	var db *gorm.DB
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
	// The bounds were resolved before opening so PrepareStmt matches this pool.
	sqlDB.SetMaxIdleConns(maxIdleConns)
	sqlDB.SetMaxOpenConns(maxOpenConns)
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

// normalizeMySQLDSN converts a mysql:// URL into the go-sql-driver DSN form and
// ensures the settings the schema relies on are present.
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

// hasDSNQueryKey reports whether a DSN already sets the given query parameter.
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
