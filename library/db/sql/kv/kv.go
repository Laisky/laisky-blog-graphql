package kv

import (
	"context"
	"regexp"
	"time"

	"github.com/pkg/errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	_ Interface = new(Kv)

	regexpKey = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)
)

// KvItem is a kv doc
type KvItem struct {
	Key       string    `gorm:"column:key;primaryKey" json:"key"`
	Value     string    `gorm:"column:value" json:"value"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	ExpireAt  time.Time `gorm:"column:expire_at" json:"expire_at"`
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
	db  *gorm.DB
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
		o.tableName = tableName
		return nil
	}
}

// NewKv create a new kv
func NewKv(db **gorm.DB, opts ...Option) (*Kv, error) {
	opt, err := applyOpts(opts...)
	if err != nil {
		return nil, errors.Wrap(err, "apply opts")
	}

	kv := &Kv{
		opt: opt,
		db:  *db,
	}

	if err := kv.setup(); err != nil {
		return nil, errors.Wrap(err, "setup kv")
	}

	return kv, nil
}

func (kv *Kv) setup() error {
	err := kv.db.Table(kv.opt.tableName).AutoMigrate(&KvItem{})
	return errors.Wrap(err, "auto migrate kv")
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
// Uses GORM's OnConflict clause to avoid SQL injection risks.
func (kv *Kv) SetWithExpireAt(ctx context.Context, key, value string, expireAt time.Time) error {
	if expireAt.Before(time.Now()) {
		return errors.Errorf("expire time is in the past: %s", expireAt)
	}
	if expireAt.After(time.Now().Add(time.Hour * 24 * 30)) {
		return errors.Errorf("expire time is too far in the future: %s", expireAt)
	}

	doc := KvItem{
		Key:      key,
		Value:    value,
		ExpireAt: expireAt,
	}

	return kv.db.WithContext(ctx).Table(kv.opt.tableName).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "expire_at"}),
		}).
		Create(&doc).Error
}

// Get retrieves the key's document. If the key is expired,
// it deletes the record and returns an error.
func (kv *Kv) Get(ctx context.Context, key string) (*KvItem, error) {
	var doc KvItem
	err := kv.db.WithContext(ctx).Table(kv.opt.tableName).
		Where("key = ?", key).
		First(&doc).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.Errorf("key not found: %s", key)
		}
		return nil, errors.Wrap(err, "failed to get key")
	}

	// Check expiration if expire_at is non-zero.
	if !doc.ExpireAt.IsZero() && time.Now().After(doc.ExpireAt) {
		_ = kv.Del(ctx, key)
		return nil, errors.Errorf("key expired: %s", key)
	}
	return &doc, nil
}

// Exists checks whether a key exists and hasn't expired.
func (kv *Kv) Exists(ctx context.Context, key string) (bool, error) {
	var count int64
	err := kv.db.WithContext(ctx).Table(kv.opt.tableName).
		Where("key = ? AND (expire_at IS NULL OR expire_at = '0001-01-01 00:00:00' OR expire_at > ?)", key, time.Now()).
		Count(&count).Error
	if err != nil {
		return false, errors.Wrap(err, "failed to check existence")
	}
	return count > 0, nil
}

// Del removes the key from the store.
func (kv *Kv) Del(ctx context.Context, key string) error {
	result := kv.db.WithContext(ctx).Table(kv.opt.tableName).
		Where("key = ?", key).
		Delete(&KvItem{})
	if result.Error != nil {
		return errors.Wrap(result.Error, "failed to delete key")
	}
	return nil
}
