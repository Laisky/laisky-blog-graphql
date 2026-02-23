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

	regexpKey       = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)
	regexpTableName = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)
	errKeyNotFound  = errors.New("key not found")
	errKeyExpired   = errors.New("key expired")
)

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
}

// Kv is a key-value store for postgres
type Kv struct {
	opt *option
	db  *sql.DB
}

type option struct {
	tableName string
}

// Option is a function that configures the kv
type Option func(*option) error

func applyOpts(opts ...Option) (*option, error) {
	// fill default
	o := &option{
		tableName: "kv",
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

// NewKv create a new kv
func NewKv(db *sql.DB, opts ...Option) (*Kv, error) {
	if db == nil {
		return nil, errors.New("db cannot be nil")
	}

	opt, err := applyOpts(opts...)
	if err != nil {
		return nil, errors.Wrap(err, "apply opts")
	}

	kv := &Kv{
		opt: opt,
		db:  db,
	}

	if err := kv.setup(); err != nil {
		return nil, errors.Wrap(err, "setup kv")
	}

	return kv, nil
}

func (kv *Kv) setup() error {
	stmt := `
CREATE TABLE IF NOT EXISTS ` + kv.opt.tableName + ` (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL,
  expire_at TIMESTAMP NOT NULL
)`

	if _, err := kv.db.Exec(stmt); err != nil {
		return errors.Wrap(err, "create kv table")
	}

	return nil
}

func (kv *Kv) validKey(key string) error {
	if !regexpKey.MatchString(key) {
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
	if time.Now().Add(ttl).After(time.Now().Add(time.Hour * 24 * 30)) {
		return errors.Errorf("ttl is too far in the future: %s", ttl)
	}

	if err := kv.validKey(key); err != nil {
		return errors.WithStack(err)
	}
	if err := kv.validValue(value); err != nil {
		return errors.WithStack(err)
	}
	expireAt := time.Now().Add(ttl)
	return kv.SetWithExpireAt(ctx, key, value, expireAt)
}

// SetWithExpireAt stores the key-value pair with a specific expiration time.
func (kv *Kv) SetWithExpireAt(ctx context.Context, key, value string, expireAt time.Time) error {
	if expireAt.Before(time.Now()) {
		return errors.Errorf("expire time is in the past: %s", expireAt)
	}
	if expireAt.After(time.Now().Add(time.Hour * 24 * 30)) {
		return errors.Errorf("expire time is too far in the future: %s", expireAt)
	}

	now := time.Now().UTC()
	stmt := `
INSERT INTO ` + kv.opt.tableName + ` (key, value, created_at, expire_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT(key)
DO UPDATE SET value = EXCLUDED.value, expire_at = EXCLUDED.expire_at`

	if _, err := kv.db.ExecContext(ctx, stmt, key, value, now, expireAt.UTC()); err != nil {
		return errors.Wrap(err, "upsert kv item")
	}

	return nil
}

// Get retrieves the key's document. If the key is expired,
// it deletes the record and returns an error.
func (kv *Kv) Get(ctx context.Context, key string) (*KvItem, error) {
	var doc KvItem
	stmt := `SELECT key, value, created_at, expire_at FROM ` + kv.opt.tableName + ` WHERE key = $1 LIMIT 1`
	err := kv.db.QueryRowContext(ctx, stmt, key).Scan(&doc.Key, &doc.Value, &doc.CreatedAt, &doc.ExpireAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.Wrapf(errKeyNotFound, "key %s", key)
		}
		return nil, errors.Wrap(err, "failed to get key")
	}

	// Check expiration if expire_at is non-zero.
	if !doc.ExpireAt.IsZero() && time.Now().After(doc.ExpireAt) {
		_ = kv.Del(ctx, key)
		return nil, errors.Wrapf(errKeyExpired, "key %s", key)
	}
	return &doc, nil
}

// Exists checks whether a key exists and hasn't expired.
func (kv *Kv) Exists(ctx context.Context, key string) (bool, error) {
	item, err := kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, errKeyNotFound) || errors.Is(err, errKeyExpired) {
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
	stmt := `DELETE FROM ` + kv.opt.tableName + ` WHERE key = $1`
	if _, err := kv.db.ExecContext(ctx, stmt, key); err != nil {
		return errors.Wrap(err, "failed to delete key")
	}
	return nil
}
