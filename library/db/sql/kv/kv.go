// Package kv is a small expiring key-value table on PostgreSQL or SQLite, for
// short-lived values that must commit or roll back with other SQL changes.
package kv

import (
	"context"
	"database/sql"
	"regexp"
	"time"

	errors "github.com/Laisky/errors/v2"
)

var (
	_ Interface = new(Kv)

	// regexpKey admits path-like keys such as "ns|tenant|project|/a/b.md|1";
	// maxKeyLength bounds their length.
	regexpKey       = regexp.MustCompile(`^[a-zA-Z0-9_./|-]+$`)
	regexpTableName = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)

	// ErrKeyNotFound reports that no item is stored under the key.
	ErrKeyNotFound = errors.New("key not found")
	// ErrKeyExpired reports that the item stored under the key has expired.
	ErrKeyExpired = errors.New("key expired")
)

// maxTTL bounds how far in the future an item may expire.
const maxTTL = 30 * 24 * time.Hour

// maxKeyLength bounds the length of a key in bytes.
const maxKeyLength = 1024

// KvItem is a kv doc
type KvItem struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"created_at"`
	ExpireAt  time.Time `json:"expire_at"`
}

// Interface is a kv interface
type Interface interface {
	SetWithTTL(ctx context.Context, key, value string, ttl time.Duration) error
	SetWithExpireAt(ctx context.Context, key, value string, expireAt time.Time) error
	Get(ctx context.Context, key string) (*KvItem, error)
	Exists(ctx context.Context, key string) (bool, error)
	Del(ctx context.Context, key string) error
	DeleteExpired(ctx context.Context, limit int) (int64, error)
}

// Executor runs statements on a database handle or on an open transaction.
type Executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Kv is a key-value store for postgres
type Kv struct {
	opt *option
	db  Executor
}

type option struct {
	tableName string
	now       func() time.Time
}

// Option is a function that configures the kv
type Option func(*option) error

func applyOpts(opts ...Option) (*option, error) {
	// fill default
	o := &option{
		tableName: "kv",
		now:       func() time.Time { return time.Now().UTC() },
	}

	// apply opts
	for _, opt := range opts {
		if err := opt(o); err != nil {
			return nil, errors.WithStack(err)
		}
	}

	return o, nil
}

// WithDBName is a option to set db name
func WithDBName(tableName string) Option {
	return func(o *option) error {
		if !regexpTableName.MatchString(tableName) {
			return errors.Errorf("invalid table name: %s", tableName)
		}
		o.tableName = tableName
		return nil
	}
}

// WithClock sets the clock that stamps and expires items. The default is the
// UTC wall clock.
func WithClock(now func() time.Time) Option {
	return func(o *option) error {
		if now == nil {
			return errors.New("clock cannot be nil")
		}
		o.now = func() time.Time { return now().UTC() }
		return nil
	}
}

// NewKv creates the table when it is missing and returns a store on db.
func NewKv(ctx context.Context, db Executor, opts ...Option) (*Kv, error) {
	if err := CreateTable(ctx, db, opts...); err != nil {
		return nil, errors.Wrap(err, "setup kv")
	}
	return Open(db, opts...)
}

// Open returns a store on db without running DDL. Use it when CreateTable ran
// in a schema migration.
func Open(db Executor, opts ...Option) (*Kv, error) {
	if db == nil {
		return nil, errors.New("db cannot be nil")
	}

	opt, err := applyOpts(opts...)
	if err != nil {
		return nil, errors.Wrap(err, "apply opts")
	}

	return &Kv{opt: opt, db: db}, nil
}

// CreateTable creates the table and its expiration index when they are
// missing. db may be a transaction, so the DDL can commit with a migration.
func CreateTable(ctx context.Context, db Executor, opts ...Option) error {
	if db == nil {
		return errors.New("db cannot be nil")
	}
	opt, err := applyOpts(opts...)
	if err != nil {
		return errors.Wrap(err, "apply opts")
	}

	//nolint:gosec // G202: table name is not user input, validated by regexpTableName
	stmts := []string{`
CREATE TABLE IF NOT EXISTS ` + opt.tableName + ` (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL,
  expire_at TIMESTAMP NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS ` + opt.tableName + `_expire_at_idx ON ` + opt.tableName + ` (expire_at)`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return errors.Wrap(err, "create kv table")
		}
	}

	return nil
}

// WithTx returns a copy of the store whose statements run on tx, so they
// commit or roll back with the caller's other changes.
func (kv *Kv) WithTx(tx Executor) *Kv {
	return &Kv{opt: kv.opt, db: tx}
}

func (kv *Kv) validKey(key string) error {
	if len(key) > maxKeyLength || !regexpKey.MatchString(key) {
		return errors.Errorf("invalid key: %s", key)
	}

	return nil
}

func (kv *Kv) validValue(_ string) error {
	return nil
}

// SetWithTTL stores the key-value pair with a time-to-live duration.
func (kv *Kv) SetWithTTL(ctx context.Context, key, value string, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.Errorf("ttl must be greater than 0: %s", ttl)
	}
	if ttl > maxTTL {
		return errors.Errorf("ttl is too far in the future: %s", ttl)
	}

	if err := kv.validKey(key); err != nil {
		return errors.WithStack(err)
	}
	if err := kv.validValue(value); err != nil {
		return errors.WithStack(err)
	}
	return kv.SetWithExpireAt(ctx, key, value, kv.opt.now().Add(ttl))
}

// SetWithExpireAt stores the key-value pair with a specific expiration time.
func (kv *Kv) SetWithExpireAt(ctx context.Context, key, value string, expireAt time.Time) error {
	now := kv.opt.now()
	if expireAt.Before(now) {
		return errors.Errorf("expire time is in the past: %s", expireAt)
	}
	if expireAt.After(now.Add(maxTTL)) {
		return errors.Errorf("expire time is too far in the future: %s", expireAt)
	}
	if err := kv.validKey(key); err != nil {
		return errors.WithStack(err)
	}

	//nolint:gosec // G202: table name is not user input, validated by regexpTableName
	stmt := `
INSERT INTO ` + kv.opt.tableName + ` (key, value, created_at, expire_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT(key)
DO UPDATE SET value = EXCLUDED.value, created_at = EXCLUDED.created_at, expire_at = EXCLUDED.expire_at`

	if _, err := kv.db.ExecContext(ctx, stmt, key, value, now, expireAt.UTC()); err != nil {
		return errors.Wrap(err, "upsert kv item")
	}

	return nil
}

// Get retrieves the key's document. If the key is expired,
// it deletes the record and returns an error.
func (kv *Kv) Get(ctx context.Context, key string) (*KvItem, error) {
	var doc KvItem
	stmt := `SELECT key, value, created_at, expire_at FROM ` + kv.opt.tableName + ` WHERE key = $1 LIMIT 1` //nolint:gosec // G202: table name is not user input, validated by regexpTableName
	err := kv.db.QueryRowContext(ctx, stmt, key).Scan(&doc.Key, &doc.Value, &doc.CreatedAt, &doc.ExpireAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.Wrapf(ErrKeyNotFound, "key %s", key)
		}
		return nil, errors.Wrap(err, "failed to get key")
	}

	// Check expiration if expire_at is non-zero.
	if !doc.ExpireAt.IsZero() && kv.opt.now().After(doc.ExpireAt) {
		if err := kv.Del(ctx, key); err != nil {
			return nil, errors.Wrapf(err, "delete expired key %s", key)
		}
		return nil, errors.Wrapf(ErrKeyExpired, "key %s", key)
	}
	return &doc, nil
}

// Exists checks whether a key exists and hasn't expired.
func (kv *Kv) Exists(ctx context.Context, key string) (bool, error) {
	item, err := kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, ErrKeyNotFound) || errors.Is(err, ErrKeyExpired) {
			return false, nil
		}
		return false, errors.Wrap(err, "failed to check existence")
	}

	if item == nil {
		return false, nil
	}

	return true, nil
}

// Del removes the key from the store.
func (kv *Kv) Del(ctx context.Context, key string) error {
	stmt := `DELETE FROM ` + kv.opt.tableName + ` WHERE key = $1` //nolint:gosec // G202: table name is not user input, validated by regexpTableName
	if _, err := kv.db.ExecContext(ctx, stmt, key); err != nil {
		return errors.Wrap(err, "failed to delete key")
	}
	return nil
}

// DeleteExpired removes at most limit expired items and reports how many it
// removed, so a periodic sweep keeps each statement bounded.
func (kv *Kv) DeleteExpired(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		return 0, errors.Errorf("limit must be greater than 0: %d", limit)
	}
	//nolint:gosec // G202: table name is not user input, validated by regexpTableName
	stmt := `DELETE FROM ` + kv.opt.tableName + ` WHERE key IN (
  SELECT key FROM ` + kv.opt.tableName + ` WHERE expire_at < $1 ORDER BY expire_at LIMIT $2)`
	result, err := kv.db.ExecContext(ctx, stmt, kv.opt.now(), limit)
	if err != nil {
		return 0, errors.Wrap(err, "delete expired kv items")
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, errors.Wrap(err, "count deleted kv items")
	}
	return removed, nil
}
