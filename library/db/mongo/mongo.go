// Package mongo provides a wrapper for the MongoDB client.
package mongo

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Laisky/laisky-blog-graphql/library/log"

	"github.com/Laisky/errors/v2"
	"github.com/Laisky/zap"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

const (
	defaultTimeout         = 30 * time.Second
	reconnectCheckInterval = 5 * time.Second
)

type DB interface {
	Close(ctx context.Context) error
	GetCol(colName string) *mongo.Collection
	DB(name string) *mongo.Database
	CurrentDB() *mongo.Database
}

// type Collection interface {
// 	Find(ctx context.Context, filter interface{},
// 		opts ...*options.FindOptions) (cur *mongo.Cursor, err error)
// 	FindOne(ctx context.Context, filter interface{},
// 		opts ...*options.FindOneOptions) *mongo.SingleResult
// }

type DialInfo struct {
	Addr,
	DBName,
	User,
	Pwd string
}

type db struct {
	sync.RWMutex
	cli     *mongo.Client
	diaInfo DialInfo
	uri     string
}

func NewDB(ctx context.Context,
	dialInfo DialInfo,
) (DB, error) {
	log.Logger.Info("try to connect to mongodb",
		zap.String("addr", dialInfo.Addr),
		zap.String("db", dialInfo.DBName),
	)
	db := &db{
		diaInfo: dialInfo,
		uri:     fmt.Sprintf("mongodb://%s:%s@%s/%s", dialInfo.User, dialInfo.Pwd, dialInfo.Addr, dialInfo.DBName),
	}

	if err := db.dial(ctx); err != nil {
		return nil, errors.Wrap(err, "connect")
	}

	go db.runReconnectCheck(ctx)
	return db, nil
}

func (d *db) dial(ctx context.Context) (err error) {
	d.Lock()
	defer d.Unlock()

	d.cli, err = mongo.Connect(ctx, options.Client().ApplyURI(d.uri))
	if err != nil {
		return errors.Wrap(err, "connect db")
	}

	return nil
}

func (d *db) DB(name string) *mongo.Database {
	d.RLock()
	defer d.RUnlock()

	return d.cli.Database(name)
}

func (d *db) S() (mongo.Session, error) {
	d.RLock()
	defer d.RUnlock()

	return d.cli.StartSession()
}

func (d *db) CurrentDB() *mongo.Database {
	d.RLock()
	defer d.RUnlock()

	return d.DB(d.diaInfo.DBName)
}

func (d *db) runReconnectCheck(ctx context.Context) {
	var err error
	ticker := time.NewTicker(reconnectCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if err = d.cli.Ping(ctx, readpref.Primary()); err != nil {
			log.Logger.Error("db connection got error", zap.Error(err), zap.String("db", d.diaInfo.Addr))
			if err = d.dial(ctx); err != nil {
				log.Logger.Error("can not reconnect to db", zap.Error(err), zap.String("db", d.diaInfo.Addr))
				time.Sleep(3 * time.Second)
				continue
			}

			log.Logger.Info("success reconnect to db", zap.String("db", d.diaInfo.Addr))
		}
	}
}

func (d *db) Close(ctx context.Context) error {
	return d.cli.Disconnect(ctx)
}

func (d *db) GetCol(colName string) *mongo.Collection {
	return d.CurrentDB().Collection(colName)
}
