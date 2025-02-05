package redis

import (
	gredis "github.com/Laisky/go-redis/v2"
	"github.com/redis/go-redis/v9"
)

// DB is a wrapper for go-redis
type DB struct {
	db *gredis.Utils
}

// NewDB creates a new DB instance
func NewDB(opt *redis.Options) *DB {
	rdb := redis.NewClient(opt)
	rutils := gredis.NewRedisUtils(rdb)

	return &DB{
		db: rutils,
	}
}
