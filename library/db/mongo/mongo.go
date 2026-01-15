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

// DB is the same exportable interface. Do not change.
type DB interface {
	Close(ctx context.Context) error
	GetCol(colName string) *mongo.Collection
	DB(name string) *mongo.Database
	CurrentDB() *mongo.Database
}

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

// NewDB keeps the same signature and behavior expectations.
// It creates ONE long-lived mongo.Client and relies on the driver for reconnects.
func NewDB(ctx context.Context, dialInfo DialInfo) (DB, error) {
	log.Logger.Info("try to connect to mongodb",
		zap.String("addr", dialInfo.Addr),
		zap.String("db", dialInfo.DBName),
	)

	d := &db{
		diaInfo: dialInfo,
		uri:     fmt.Sprintf("mongodb://%s:%s@%s/%s", dialInfo.User, dialInfo.Pwd, dialInfo.Addr, dialInfo.DBName),
	}

	if err := d.dial(ctx); err != nil {
		return nil, errors.Wrap(err, "connect")
	}

	// Keep this goroutine to preserve the original side effect (a background checker),
	// but make it SAFE: it never reconnects or creates new clients.
	// It only logs health signals.
	go d.runReconnectCheck(ctx)

	return d, nil
}

// dial creates the client ONCE.
// Best practice: use the driver’s pooling and monitoring, avoid manual reconnect loops.
func (d *db) dial(ctx context.Context) error {
	// Use a bounded timeout for initial connect + ping.
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	clientOpts := options.Client().
		ApplyURI(d.uri).
		// Timeouts and stability (good defaults for most services)
		SetConnectTimeout(30 * time.Second).
		SetServerSelectionTimeout(30 * time.Second).
		SetSocketTimeout(30 * time.Second).
		// Pooling (avoid runaway threads/FDs under load)
		SetMaxPoolSize(100).
		SetMinPoolSize(0).
		SetMaxConnecting(2).
		SetMaxConnIdleTime(300 * time.Second)

	cli, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		return errors.Wrap(err, "connect db")
	}

	// Force a first server selection now so failures happen at startup, not later.
	if err := cli.Ping(ctx, readpref.Primary()); err != nil {
		_ = cli.Disconnect(context.Background())
		return errors.Wrap(err, "ping db")
	}

	d.Lock()
	// Defensive: if somehow called twice, close the old client before replacing.
	if d.cli != nil {
		_ = d.cli.Disconnect(context.Background())
	}
	d.cli = cli
	d.Unlock()

	return nil
}

func (d *db) DB(name string) *mongo.Database {
	d.RLock()
	cli := d.cli
	d.RUnlock()

	// If cli is nil, this is a programmer error; keep behavior simple.
	return cli.Database(name)
}

// S is intentionally NOT part of the exportable DB interface.
// Keep it for compatibility with existing internal users.
func (d *db) S() (mongo.Session, error) {
	d.RLock()
	cli := d.cli
	d.RUnlock()

	return cli.StartSession()
}

func (d *db) CurrentDB() *mongo.Database {
	return d.DB(d.diaInfo.DBName)
}

// runReconnectCheck keeps the same method name and is still started in NewDB,
// but it no longer reconnects (that was the bug).
// It only performs a lightweight health check and logs when the server is unreachable.
// The driver will recover connections automatically when the server comes back.
func (d *db) runReconnectCheck(ctx context.Context) {
	ticker := time.NewTicker(reconnectCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		d.RLock()
		cli := d.cli
		d.RUnlock()
		if cli == nil {
			continue
		}

		// Never use the long-lived ctx directly for ping; always bound it.
		pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := cli.Ping(pingCtx, readpref.Primary())
		cancel()

		if err != nil {
			log.Logger.Warn("mongodb ping failed (driver will auto-recover)",
				zap.Error(err),
				zap.String("db", d.diaInfo.Addr),
			)
		}
	}
}

func (d *db) Close(ctx context.Context) error {
	d.Lock()
	cli := d.cli
	d.cli = nil
	d.Unlock()

	if cli == nil {
		return nil
	}

	// Bound shutdown time to avoid hanging on exit.
	if ctx == nil {
		ctx = context.Background()
	}
	closeCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	return cli.Disconnect(closeCtx)
}

func (d *db) GetCol(colName string) *mongo.Collection {
	return d.CurrentDB().Collection(colName)
}
