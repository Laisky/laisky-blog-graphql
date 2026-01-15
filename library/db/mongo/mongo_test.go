package mongo

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// resetSharedClientsForTest clears shared clients between tests.
func resetSharedClientsForTest() {
	sharedClientsMu.Lock()
	sharedClients = map[string]*sharedClient{}
	sharedClientsMu.Unlock()
}

// TestNewDBSharesClient verifies that NewDB reuses the same shared client.
func TestNewDBSharesClient(t *testing.T) {
	resetSharedClientsForTest()

	oldConnect := connectMongo
	oldPing := pingMongo
	oldDisconnect := disconnectMongo
	var connectCount int32
	var disconnectCount int32

	connectMongo = func(ctx context.Context, clientOpts *options.ClientOptions) (*mongo.Client, error) {
		atomic.AddInt32(&connectCount, 1)
		cli, err := mongo.NewClient(options.Client().ApplyURI("mongodb://example.com"))
		if err != nil {
			return nil, errors.Wrap(err, "new client")
		}
		return cli, nil
	}
	pingMongo = func(ctx context.Context, cli *mongo.Client) error {
		return nil
	}
	disconnectMongo = func(ctx context.Context, cli *mongo.Client) error {
		atomic.AddInt32(&disconnectCount, 1)
		return nil
	}

	t.Cleanup(func() {
		connectMongo = oldConnect
		pingMongo = oldPing
		disconnectMongo = oldDisconnect
		resetSharedClientsForTest()
	})

	ctx := context.Background()
	dial := DialInfo{
		Addr:   "localhost:27017",
		DBName: "db",
		User:   "user",
		Pwd:    "pwd",
	}

	db1, err := NewDB(ctx, dial)
	require.NoError(t, err)
	db2, err := NewDB(ctx, dial)
	require.NoError(t, err)

	require.Equal(t, int32(1), atomic.LoadInt32(&connectCount))

	d1 := db1.(*db)
	d2 := db2.(*db)
	require.Same(t, d1.shared, d2.shared)

	require.NoError(t, db1.Close(ctx))
	require.Equal(t, int32(0), atomic.LoadInt32(&disconnectCount))

	require.NoError(t, db2.Close(ctx))
	require.Equal(t, int32(1), atomic.LoadInt32(&disconnectCount))
}

// TestNewDBSharesClientAcrossDatabases verifies that different database names share the same client.
func TestNewDBSharesClientAcrossDatabases(t *testing.T) {
	resetSharedClientsForTest()

	oldConnect := connectMongo
	oldPing := pingMongo

	var connectCount int32
	connectMongo = func(ctx context.Context, clientOpts *options.ClientOptions) (*mongo.Client, error) {
		atomic.AddInt32(&connectCount, 1)
		cli, err := mongo.NewClient(options.Client().ApplyURI("mongodb://example.com"))
		if err != nil {
			return nil, errors.Wrap(err, "new client")
		}
		return cli, nil
	}
	pingMongo = func(ctx context.Context, cli *mongo.Client) error {
		return nil
	}

	t.Cleanup(func() {
		connectMongo = oldConnect
		pingMongo = oldPing
		resetSharedClientsForTest()
	})

	ctx := context.Background()
	dialA := DialInfo{Addr: "localhost:27017", DBName: "dbA", User: "user", Pwd: "pwd"}
	dialB := DialInfo{Addr: "localhost:27017", DBName: "dbB", User: "user", Pwd: "pwd"}

	dbA, err := NewDB(ctx, dialA)
	require.NoError(t, err)
	dbB, err := NewDB(ctx, dialB)
	require.NoError(t, err)

	require.Same(t, dbA.(*db).shared, dbB.(*db).shared)
	require.Equal(t, int32(1), atomic.LoadInt32(&connectCount))
	require.Equal(t, "dbA", dbA.CurrentDB().Name())
	require.Equal(t, "dbB", dbB.CurrentDB().Name())

	require.NoError(t, dbA.Close(ctx))
	require.NoError(t, dbB.Close(ctx))
}

// TestNewDBDifferentURI verifies that different auth settings create separate clients.
func TestNewDBDifferentURI(t *testing.T) {
	resetSharedClientsForTest()

	oldConnect := connectMongo
	oldPing := pingMongo

	var connectCount int32
	connectMongo = func(ctx context.Context, clientOpts *options.ClientOptions) (*mongo.Client, error) {
		atomic.AddInt32(&connectCount, 1)
		cli, err := mongo.NewClient(options.Client().ApplyURI("mongodb://example.com"))
		if err != nil {
			return nil, errors.Wrap(err, "new client")
		}
		return cli, nil
	}
	pingMongo = func(ctx context.Context, cli *mongo.Client) error {
		return nil
	}

	t.Cleanup(func() {
		connectMongo = oldConnect
		pingMongo = oldPing
		resetSharedClientsForTest()
	})

	ctx := context.Background()
	dialA := DialInfo{Addr: "localhost:27017", DBName: "dbA", User: "userA", Pwd: "pwd"}
	dialB := DialInfo{Addr: "localhost:27017", DBName: "dbB", User: "userB", Pwd: "pwd"}

	dbA, err := NewDB(ctx, dialA)
	require.NoError(t, err)
	dbB, err := NewDB(ctx, dialB)
	require.NoError(t, err)

	require.NotSame(t, dbA.(*db).shared, dbB.(*db).shared)
	require.Equal(t, int32(2), atomic.LoadInt32(&connectCount))

	require.NoError(t, dbA.Close(ctx))
	require.NoError(t, dbB.Close(ctx))
}
