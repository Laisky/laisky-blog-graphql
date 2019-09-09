package models

import (
	"context"
	"sync"
	"time"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
	"gopkg.in/mgo.v2"
)

const (
	defaultTimeout         = 3 * time.Second
	reconnectCheckInterval = 5 * time.Second
)

func NewMongoDB(ctx context.Context, addr string) (db *DB, err error) {
	db = &DB{}
	if err = db.dial(addr); err != nil {
		return nil, err
	}
	go db.runReconnectCheck(ctx, addr)
	return db, nil
}

type DB struct {
	sync.RWMutex
	s *mgo.Session
}

func (d *DB) dial(addr string) error {
	d.Lock()
	defer d.Unlock()

	s, err := mgo.DialWithTimeout(addr, defaultTimeout)
	if err != nil {
		return errors.Wrap(err, "can not connect to db")
	}

	d.s = s
	return nil
}

func (d *DB) GetDB(name string) *mgo.Database {
	d.RLock()
	defer d.RUnlock()

	return d.s.DB(name)
}

func (d *DB) runReconnectCheck(ctx context.Context, addr string) {
	var err error
	ticker := time.Tick(reconnectCheckInterval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker:
		}

		if err = d.s.Ping(); err != nil {
			utils.Logger.Error("db connection got error", zap.Error(err), zap.String("db", addr))
			if err = d.dial(addr); err != nil {
				utils.Logger.Error("can not reconnect to db", zap.Error(err), zap.String("db", addr))
				continue
			}
			utils.Logger.Info("success reconnect to db", zap.String("db", addr))
		}
	}
}

func (d *DB) Close() {
	d.s.Close()
}

func (d *DB) GetCol(dbName, colName string) *mgo.Collection {
	return d.GetDB(dbName).C(colName)
}
