package mongo

import (
	"context"
	"sync"
	"time"

	"github.com/Laisky/laisky-blog-graphql/library/log"

	"github.com/Laisky/zap"
	"github.com/pkg/errors"
	"gopkg.in/mgo.v2"
)

const (
	defaultTimeout         = 30 * time.Second
	reconnectCheckInterval = 5 * time.Second
)

type DB interface {
	GetDB() *mgo.Database
	Close()
	GetCol(colName string) *mgo.Collection
}

type db struct {
	sync.RWMutex
	s      *mgo.Session
	dbName string
}

func NewDB(ctx context.Context, addr, dbName, user, pwd string) (DB, error) {
	log.Logger.Info("try to connect to mongodb",
		zap.String("addr", addr),
		zap.String("db", dbName),
	)
	db := &db{
		dbName: dbName,
	}

	dialInfo := &mgo.DialInfo{
		Addrs:     []string{addr},
		Direct:    true,
		Timeout:   defaultTimeout,
		Database:  dbName,
		Username:  user,
		Password:  pwd,
		PoolLimit: 1000,
	}

	if err := db.dial(dialInfo); err != nil {
		return nil, err
	}
	go db.runReconnectCheck(ctx, dialInfo)
	return db, nil
}

func (d *db) dial(dialInfo *mgo.DialInfo) error {
	d.Lock()
	defer d.Unlock()

	s, err := mgo.DialWithInfo(dialInfo)
	if err != nil {
		return errors.Wrap(err, "can not connect to db")
	}

	d.s = s
	return nil
}

func (d *db) GetDB() *mgo.Database {
	d.RLock()
	defer d.RUnlock()

	return d.s.DB("")
}

func (d *db) runReconnectCheck(ctx context.Context, dialInfo *mgo.DialInfo) {
	var err error
	ticker := time.NewTicker(reconnectCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if err = d.s.Ping(); err != nil {
			log.Logger.Error("db connection got error", zap.Error(err), zap.Strings("db", dialInfo.Addrs))
			if err = d.dial(dialInfo); err != nil {
				log.Logger.Error("can not reconnect to db", zap.Error(err), zap.Strings("db", dialInfo.Addrs))
				time.Sleep(3 * time.Second)
				continue
			}
			log.Logger.Info("success reconnect to db", zap.Strings("db", dialInfo.Addrs))
		}
	}
}

func (d *db) Close() {
	d.s.Close()
}

func (d *db) GetCol(colName string) *mgo.Collection {
	return d.GetDB().C(colName)
}
