package pageindex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
	bolt "go.etcd.io/bbolt"
)

// Cache stores LLM responses keyed by sha256(version, model, prompt, schema, params).
type Cache interface {
	Get(key [32]byte) (*Response, bool, error)
	Put(key [32]byte, resp *Response) error
	Close() error
}

// CacheConfig holds the bbolt cache parameters.
type CacheConfig struct {
	Enabled      bool
	Path         string
	MaxSizeBytes int64
}

const cacheBucket = "responses"

// NewCache opens the bbolt database. When cfg.Enabled is false a no-op cache is
// returned so callers do not have to nil-check.
func NewCache(cfg CacheConfig) (Cache, error) {
	if !cfg.Enabled {
		return &noopCache{}, nil
	}
	if cfg.Path == "" {
		return nil, errors.New("cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o755); err != nil {
		return nil, errors.Wrap(err, "create cache dir")
	}
	db, err := bolt.Open(cfg.Path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, errors.Wrap(err, "open bbolt cache")
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, e := tx.CreateBucketIfNotExists([]byte(cacheBucket))
		return e
	}); err != nil {
		_ = db.Close()
		return nil, errors.Wrap(err, "ensure cache bucket")
	}
	return &boltCache{db: db, max: cfg.MaxSizeBytes}, nil
}

// CacheKey hashes the canonical tuple (algo version, model, prompt, schema, extra params).
func CacheKey(model, prompt string, schema, params []byte) [32]byte {
	h := sha256.New()
	h.Write([]byte(AlgorithmVersion))
	h.Write([]byte{0})
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write([]byte(prompt))
	h.Write([]byte{0})
	h.Write(schema)
	h.Write([]byte{0})
	h.Write(params)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

type cachedRecord struct {
	Output   json.RawMessage `json:"output"`
	Text     string          `json:"text"`
	Usage    Usage           `json:"usage"`
	CachedAt time.Time       `json:"cached_at"`
}

type boltCache struct {
	db  *bolt.DB
	max int64
	mu  sync.RWMutex
}

func (c *boltCache) Get(key [32]byte) (*Response, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var rec *cachedRecord
	err := c.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(cacheBucket))
		if b == nil {
			return nil
		}
		raw := b.Get(key[:])
		if raw == nil {
			return nil
		}
		var r cachedRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			return errors.Wrap(err, "decode cached record")
		}
		rec = &r
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if rec == nil {
		return nil, false, nil
	}
	return &Response{Output: rec.Output, Text: rec.Text, Usage: rec.Usage, CacheHit: true}, true, nil
}

func (c *boltCache) Put(key [32]byte, resp *Response) error {
	if resp == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.max > 0 {
		if size, err := c.dbSize(); err == nil && size > c.max {
			return nil
		}
	}
	rec := cachedRecord{Output: resp.Output, Text: resp.Text, Usage: resp.Usage, CachedAt: time.Now().UTC()}
	raw, err := json.Marshal(rec)
	if err != nil {
		return errors.Wrap(err, "encode cached record")
	}
	return c.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(cacheBucket))
		if b == nil {
			return errors.New("cache bucket missing")
		}
		return b.Put(key[:], raw)
	})
}

func (c *boltCache) Close() error {
	if c.db == nil {
		return nil
	}
	return c.db.Close()
}

func (c *boltCache) dbSize() (int64, error) {
	stat, err := os.Stat(c.db.Path())
	if err != nil {
		return 0, err
	}
	return stat.Size(), nil
}

// HexKey returns the hex-encoded cache key for log lines.
func HexKey(key [32]byte) string {
	return hex.EncodeToString(key[:])
}

type noopCache struct{}

func (noopCache) Get(key [32]byte) (*Response, bool, error) { return nil, false, nil }
func (noopCache) Put(key [32]byte, resp *Response) error    { return nil }
func (noopCache) Close() error                              { return nil }
