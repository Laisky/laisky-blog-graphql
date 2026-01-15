// Package mongo provides a wrapper for the MongoDB client.
package mongo

import (
	"context"
	"fmt"
	"net/url"
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
	defaultHeartbeat       = 10 * time.Second
)

// DB is the same exportable interface. Do not change.
type DB interface {
	Close(ctx context.Context) error
	GetCol(colName string) *mongo.Collection
	DB(name string) *mongo.Database
	CurrentDB() *mongo.Database
}

// DialInfo defines the MongoDB connection information.
type DialInfo struct {
	Addr,
	DBName,
	User,
	Pwd string
	AuthDB string
}

// db implements DB and shares a long-lived client.
type db struct {
	shared   *sharedClient
	dialInfo DialInfo
}

// sharedClient is a shared mongo client with ref-count.
type sharedClient struct {
	mu        sync.RWMutex
	cli       *mongo.Client
	addr      string
	uri       string
	key       string
	refCount  int
	ready     chan struct{}
	err       error
	checkOnce sync.Once
	cancel    context.CancelFunc
}

var (
	sharedClientsMu sync.Mutex
	sharedClients   = map[string]*sharedClient{}
)

var (
	connectMongo = func(ctx context.Context, clientOpts *options.ClientOptions) (*mongo.Client, error) {
		return mongo.Connect(ctx, clientOpts)
	}
	pingMongo = func(ctx context.Context, cli *mongo.Client) error {
		return cli.Ping(ctx, readpref.Primary())
	}
	disconnectMongo = func(ctx context.Context, cli *mongo.Client) error {
		return cli.Disconnect(ctx)
	}
)

// buildMongoURI builds a MongoDB connection URI from the given dial info.
func buildMongoURI(dialInfo DialInfo) string {
	uri := &url.URL{
		Scheme: "mongodb",
		Host:   dialInfo.Addr,
		Path:   "/" + dialInfo.DBName,
	}
	if dialInfo.User != "" || dialInfo.Pwd != "" {
		uri.User = url.UserPassword(dialInfo.User, dialInfo.Pwd)
	}
	if dialInfo.AuthDB != "" {
		query := url.Values{}
		query.Set("authSource", dialInfo.AuthDB)
		uri.RawQuery = query.Encode()
	}
	return uri.String()
}

// sharedClientKey builds a stable key for shared client lookup.
// It takes the dial info and returns a key string scoped to the host and auth settings.
func sharedClientKey(dialInfo DialInfo) string {
	if dialInfo.AuthDB == "" {
		return fmt.Sprintf("%s|%s|%s", dialInfo.Addr, dialInfo.User, dialInfo.Pwd)
	}

	return fmt.Sprintf("%s|%s|%s|%s", dialInfo.Addr, dialInfo.User, dialInfo.Pwd, dialInfo.AuthDB)
}

// getOrCreateSharedClient returns a shared client for the given dial info.
// It guarantees that only one mongo.Client is created per URI at a time.
func getOrCreateSharedClient(ctx context.Context, dialInfo DialInfo) (*sharedClient, error) {
	key := sharedClientKey(dialInfo)
	uri := buildMongoURI(dialInfo)

	sharedClientsMu.Lock()
	if sc := sharedClients[key]; sc != nil {
		sc.refCount++
		ready := sc.ready
		sharedClientsMu.Unlock()

		select {
		case <-ready:
		case <-ctx.Done():
			sharedClientsMu.Lock()
			sc.refCount--
			if sc.refCount == 0 && sc.cli == nil {
				delete(sharedClients, key)
			}
			sharedClientsMu.Unlock()
			return nil, errors.Wrap(ctx.Err(), "wait for mongo connect")
		}
		if sc.err != nil {
			sharedClientsMu.Lock()
			sc.refCount--
			if sc.refCount == 0 && sc.cli == nil {
				delete(sharedClients, key)
			}
			sharedClientsMu.Unlock()
			return nil, errors.Wrap(sc.err, "connect")
		}

		return sc, nil
	}

	sc := &sharedClient{
		addr:     dialInfo.Addr,
		uri:      uri,
		key:      key,
		refCount: 1,
		ready:    make(chan struct{}),
	}
	sharedClients[key] = sc
	sharedClientsMu.Unlock()

	if err := sc.dial(ctx); err != nil {
		sc.err = err
		close(sc.ready)

		sharedClientsMu.Lock()
		if sharedClients[key] == sc {
			delete(sharedClients, key)
		}
		sharedClientsMu.Unlock()

		return nil, errors.Wrap(err, "connect")
	}

	sc.err = nil
	close(sc.ready)
	sc.startHealthCheck()
	return sc, nil
}

// NewDB keeps the same signature and behavior expectations.
// It creates ONE long-lived mongo.Client and relies on the driver for reconnects.
func NewDB(ctx context.Context, dialInfo DialInfo) (DB, error) {
	log.Logger.Info("try to connect to mongodb",
		zap.String("addr", dialInfo.Addr),
		zap.String("db", dialInfo.DBName),
	)

	shared, err := getOrCreateSharedClient(ctx, dialInfo)
	if err != nil {
		return nil, errors.Wrap(err, "connect")
	}

	return &db{shared: shared, dialInfo: dialInfo}, nil
}

// dial creates the client ONCE.
// Best practice: use the driver’s pooling and monitoring, avoid manual reconnect loops.
func (s *sharedClient) dial(ctx context.Context) error {
	// Use a bounded timeout for initial connect + ping.
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	clientOpts := options.Client().
		ApplyURI(s.uri).
		// Timeouts and stability (good defaults for most services)
		SetConnectTimeout(30 * time.Second).
		SetServerSelectionTimeout(30 * time.Second).
		SetSocketTimeout(30 * time.Second).
		SetHeartbeatInterval(defaultHeartbeat).
		SetRetryReads(true).
		SetRetryWrites(true).
		// Pooling (avoid runaway threads/FDs under load)
		SetMaxPoolSize(100).
		SetMinPoolSize(0).
		SetMaxConnecting(2).
		SetMaxConnIdleTime(300 * time.Second)

	cli, err := connectMongo(ctx, clientOpts)
	if err != nil {
		return errors.Wrap(err, "connect db")
	}

	// Force a first server selection now so failures happen at startup, not later.
	if err := pingMongo(ctx, cli); err != nil {
		_ = disconnectMongo(context.Background(), cli)
		return errors.Wrap(err, "ping db")
	}

	s.mu.Lock()
	// Defensive: if somehow called twice, close the old client before replacing.
	if s.cli != nil {
		_ = disconnectMongo(context.Background(), s.cli)
	}
	s.cli = cli
	s.mu.Unlock()

	return nil
}

// DB returns a database handle for the specified name.
func (d *db) DB(name string) *mongo.Database {
	cli := d.shared.client()

	// If cli is nil, this is a programmer error; keep behavior simple.
	return cli.Database(name)
}

// S returns a new client session for transactions.
// It is intentionally NOT part of the exportable DB interface.
// Keep it for compatibility with existing internal users.
func (d *db) S() (mongo.Session, error) {
	cli := d.shared.client()

	return cli.StartSession()
}

// CurrentDB returns the database based on the dial info.
func (d *db) CurrentDB() *mongo.Database {
	return d.DB(d.dialInfo.DBName)
}

// startHealthCheck starts a single background health checker.
func (s *sharedClient) startHealthCheck() {
	s.checkOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		go s.runReconnectCheck(ctx)
	})
}

// runReconnectCheck keeps the same method name and is still started in NewDB,
// but it no longer reconnects (that was the bug).
// It only performs a lightweight health check and logs when the server is unreachable.
// The driver will recover connections automatically when the server comes back.
func (s *sharedClient) runReconnectCheck(ctx context.Context) {
	ticker := time.NewTicker(reconnectCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		cli := s.client()
		if cli == nil {
			continue
		}

		// Never use the long-lived ctx directly for ping; always bound it.
		pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := pingMongo(pingCtx, cli)
		cancel()

		if err != nil {
			log.Logger.Warn("mongodb ping failed (driver will auto-recover)",
				zap.Error(err),
				zap.String("db", s.addr),
			)
		}
	}
}

// Close decreases the ref-count and disconnects when the last user closes.
func (d *db) Close(ctx context.Context) error {
	if d.shared == nil {
		return nil
	}

	sharedClientsMu.Lock()
	d.shared.refCount--
	refCount := d.shared.refCount
	if refCount == 0 {
		delete(sharedClients, d.shared.key)
	}
	sharedClientsMu.Unlock()

	if refCount > 0 {
		return nil
	}

	if d.shared.cancel != nil {
		d.shared.cancel()
	}

	cli := d.shared.client()
	if cli == nil {
		return nil
	}

	// Bound shutdown time to avoid hanging on exit.
	if ctx == nil {
		ctx = context.Background()
	}
	closeCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	err := disconnectMongo(closeCtx, cli)
	d.shared.mu.Lock()
	d.shared.cli = nil
	d.shared.mu.Unlock()
	return err
}

// GetCol returns a collection handle by name.
func (d *db) GetCol(colName string) *mongo.Collection {
	return d.CurrentDB().Collection(colName)
}

// client returns the underlying Mongo client.
func (s *sharedClient) client() *mongo.Client {
	s.mu.RLock()
	cli := s.cli
	s.mu.RUnlock()
	return cli
}
