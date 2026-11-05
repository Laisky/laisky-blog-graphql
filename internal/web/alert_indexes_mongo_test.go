package web

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gmw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	telegramDao "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dao"
	telegramSvc "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
)

// lookupPlan is what explain("executionStats") reports for one lookup.
type lookupPlan struct {
	stages, indexes []string
	docsExamined    int64
	keysExamined    int64
}

// explainLookup explains find(filter).limit(1) on collection, the command
// FindOne sends, and collects every stage and index name in the winning plan.
func explainLookup(t *testing.T, ctx context.Context, db *mongo.Database, collection string, filter bson.D) lookupPlan {
	t.Helper()
	var explain bson.M
	require.NoError(t, db.RunCommand(ctx, bson.D{
		{Key: "explain", Value: bson.D{{Key: "find", Value: collection}, {Key: "filter", Value: filter}, {Key: "limit", Value: 1}}},
		{Key: "verbosity", Value: "executionStats"},
	}).Decode(&explain))
	var plan lookupPlan
	var walk func(node any)
	walk = func(node any) {
		switch n := node.(type) {
		case bson.M:
			if stage, ok := n["stage"].(string); ok {
				plan.stages = append(plan.stages, stage)
			}
			if index, ok := n["indexName"].(string); ok {
				plan.indexes = append(plan.indexes, index)
			}
			// SBE plans nest the plan under queryPlan.
			for _, child := range []string{"queryPlan", "inputStage", "inputStages"} {
				walk(n[child])
			}
		case bson.A:
			for _, child := range n {
				walk(child)
			}
		}
	}
	queryPlanner, ok := explain["queryPlanner"].(bson.M)
	require.True(t, ok, "explain has a queryPlanner")
	walk(queryPlanner["winningPlan"])
	stats, ok := explain["executionStats"].(bson.M)
	require.True(t, ok, "explain has executionStats")
	plan.docsExamined = asInt64(t, stats["totalDocsExamined"])
	plan.keysExamined = asInt64(t, stats["totalKeysExamined"])
	return plan
}

// asInt64 reads a BSON number.
func asInt64(t *testing.T, value any) int64 {
	t.Helper()
	switch n := value.(type) {
	case int32:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	}
	t.Fatalf("not a number: %#v", value)
	return 0
}

// indexesOn returns collection's indexes by name.
func indexesOn(t *testing.T, ctx context.Context, collection *mongo.Collection) map[string]bson.M {
	t.Helper()
	cur, err := collection.Indexes().List(ctx)
	require.NoError(t, err)
	var specs []bson.M
	require.NoError(t, cur.All(ctx, &specs))
	byName := map[string]bson.M{}
	for _, spec := range specs {
		byName[spec["name"].(string)] = spec
	}
	return byName
}

// requireIndexedLookup checks that the lookup reads one index entry of index
// and one document, the opposite of a collection scan. MongoDB 8.0 may answer
// it with EXPRESS_IXSCAN, which skips regular planning.
func requireIndexedLookup(t *testing.T, plan lookupPlan, index string) {
	t.Helper()
	require.NotContains(t, plan.stages, "COLLSCAN", "stages %v", plan.stages)
	require.Contains(t, plan.indexes, index, "stages %v", plan.stages)
	require.EqualValues(t, 1, plan.docsExamined)
	require.EqualValues(t, 1, plan.keysExamined)
}

// newIndexLogger returns a context whose request logger writes to the returned
// buffer, so a test can read what EnsureIndexes logged.
func newIndexLogger(t *testing.T, ctx context.Context) (context.Context, *bytes.Buffer) {
	t.Helper()
	output := new(bytes.Buffer)
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.Lock(zapcore.AddSync(output)), zap.DebugLevel)
	logger, err := glog.NewWithName("monitor_index_test", glog.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)
	return gmw.SetLogger(ctx, logger), output
}

// TestMongoMonitorIndexesServeAlertLookups reproduces the review finding that no
// code created an index on alert_types.name: every credential check by name, on
// the GraphQL mutation, the webhook and the fallback lookup, scanned the whole
// collection, as production's explain also showed. EnsureIndexes now creates
// it, unique, together with the subscriber lookup's index, and running it again
// changes nothing.
func TestMongoMonitorIndexesServeAlertLookups(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db := newAlertMongo(t, ctx)
	const alertTypes = 300
	alertIDs := make([]primitive.ObjectID, alertTypes)
	documents, relations := make([]any, 0, alertTypes), make([]any, 0, alertTypes)
	for i := range alertTypes {
		alertIDs[i] = primitive.NewObjectID()
		documents = append(documents, bson.M{"_id": alertIDs[i], "name": fmt.Sprintf("alert_%03d", i), "push_token": fmt.Sprintf("token-%03d", i)})
		relations = append(relations, bson.M{"alert_id": alertIDs[i], "user_id": primitive.NewObjectID()})
	}
	_, err := db.Collection("alert_types").InsertMany(ctx, documents)
	require.NoError(t, err)
	_, err = db.Collection("user_alert_relations").InsertMany(ctx, relations)
	require.NoError(t, err)
	known := bson.D{{Key: "name", Value: "alert_150"}}
	// An anonymous caller's guess names no alert type, the costliest lookup.
	unknown := bson.D{{Key: "name", Value: "guessed_alert"}}

	scan := explainLookup(t, ctx, db, "alert_types", unknown)
	require.Contains(t, scan.stages, "COLLSCAN", "without the index a lookup by name scans")
	require.EqualValues(t, alertTypes, scan.docsExamined, "a guessed name reads every alert type")

	monitor := telegramDao.NewMonitor(ssoMongoDB{db})
	logCtx, output := newIndexLogger(t, ctx)
	require.NoError(t, monitor.EnsureIndexes(logCtx))
	require.Equal(t, 2, strings.Count(output.String(), `"msg":"create monitor index"`), output.String())
	before := indexesOn(t, ctx, db.Collection("alert_types"))
	require.NoError(t, monitor.EnsureIndexes(logCtx), "a second run is a no-op")
	require.Equal(t, 2, strings.Count(output.String(), `"msg":"create monitor index"`), "a second run creates nothing")
	require.NotContains(t, output.String(), `"level":"warn"`)
	require.Equal(t, before, indexesOn(t, ctx, db.Collection("alert_types")))

	names := indexesOn(t, ctx, db.Collection("alert_types"))
	require.Len(t, names, 2)
	require.Equal(t, bson.M{"name": int32(1)}, names["name_1"]["key"])
	require.Equal(t, true, names["name_1"]["unique"])
	relationIndexes := indexesOn(t, ctx, db.Collection("user_alert_relations"))
	require.Len(t, relationIndexes, 2)
	require.Equal(t, bson.M{"alert_id": int32(1)}, relationIndexes["alert_id_1"]["key"])
	require.Nil(t, relationIndexes["alert_id_1"]["unique"], "a user can subscribe to an alert type more than once")

	requireIndexedLookup(t, explainLookup(t, ctx, db, "alert_types", known), "name_1")
	guess := explainLookup(t, ctx, db, "alert_types", unknown)
	require.NotContains(t, guess.stages, "COLLSCAN")
	require.Contains(t, guess.indexes, "name_1")
	require.Zero(t, guess.docsExamined, "a guessed name reads no alert type")
	requireIndexedLookup(t, explainLookup(t, ctx, db, "user_alert_relations", bson.D{{Key: "alert_id", Value: alertIDs[150]}}), "alert_id_1")
	alert, err := monitor.ValidateTokenForAlertType(ctx, "token-150", "alert_150")
	require.NoError(t, err)
	require.Equal(t, alertIDs[150], alert.ID)
}

// TestMongoConcurrentAlertTypeCreationKeepsOneDocument reproduces what the
// missing unique index also allowed: CreateAlertType upserts by name, and
// without a unique index MongoDB lets concurrent upserts of one name each insert
// a document. Each creator then held its own push token, but a check by name
// reads only one of the documents. With the index, one creation wins and the
// others are told the name already exists.
func TestMongoConcurrentAlertTypeCreationKeepsOneDocument(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db := newAlertMongo(t, ctx)
	monitor := telegramDao.NewMonitor(ssoMongoDB{db})
	require.NoError(t, monitor.EnsureIndexes(ctx))

	const rounds, creators = 20, 16
	for round := range rounds {
		name := fmt.Sprintf("race_%02d", round)
		start := make(chan struct{})
		var created atomic.Int32
		var mu sync.Mutex
		var refusals []error
		var wg sync.WaitGroup
		for range creators {
			wg.Go(func() {
				<-start
				if _, err := monitor.CreateAlertType(ctx, name); err != nil {
					mu.Lock()
					refusals = append(refusals, err)
					mu.Unlock()
					return
				}
				created.Add(1)
			})
		}
		close(start)
		wg.Wait()
		stored, err := db.Collection("alert_types").CountDocuments(ctx, bson.M{"name": name})
		require.NoError(t, err)
		require.EqualValues(t, 1, stored, "round %d stored one alert type per name", round)
		require.EqualValues(t, 1, created.Load(), "round %d", round)
		require.Len(t, refusals, creators-1, "round %d", round)
		for _, err := range refusals {
			require.ErrorContains(t, err, "already exists")
		}
	}
}

// TestMongoMonitorIndexesKeepStoredData covers deployments whose data or
// indexes predate EnsureIndexes. It never fails the start over them, never
// touches a document or an operator's index, and still leaves lookups indexed
// wherever that does not need a change to either.
func TestMongoMonitorIndexesKeepStoredData(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	t.Run("duplicate names get an index without uniqueness", func(t *testing.T) {
		db := newAlertMongo(t, ctx)
		_, err := db.Collection("alert_types").InsertMany(ctx, []any{
			bson.M{"name": "twice", "push_token": "first-token"},
			bson.M{"name": "twice", "push_token": "second-token"},
			bson.M{"name": "once", "push_token": "only-token"},
		})
		require.NoError(t, err)
		monitor := telegramDao.NewMonitor(ssoMongoDB{db})
		logCtx, output := newIndexLogger(t, ctx)
		require.NoError(t, monitor.EnsureIndexes(logCtx))
		require.Contains(t, output.String(), `"level":"error"`)
		require.Contains(t, output.String(), "created without uniqueness")
		require.NotContains(t, output.String(), "first-token", "the log carries no push token")

		stored, err := db.Collection("alert_types").CountDocuments(ctx, bson.M{})
		require.NoError(t, err)
		require.EqualValues(t, 3, stored, "no document is touched")
		indexes := indexesOn(t, ctx, db.Collection("alert_types"))
		require.Len(t, indexes, 2)
		require.Nil(t, indexes["name_1"]["unique"])
		requireIndexedLookup(t, explainLookup(t, ctx, db, "alert_types", bson.D{{Key: "name", Value: "once"}}), "name_1")

		output.Reset()
		require.NoError(t, monitor.EnsureIndexes(logCtx), "later starts keep the index and warn")
		require.Contains(t, output.String(), `"problems":["not unique"]`)
		require.Len(t, indexesOn(t, ctx, db.Collection("alert_types")), 2)
	})

	t.Run("an operator's index on the field is kept", func(t *testing.T) {
		db := newAlertMongo(t, ctx)
		_, err := db.Collection("alert_types").Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys: bson.D{{Key: "name", Value: 1}}, Options: options.Index().SetName("alert_name_unique").SetUnique(true),
		})
		require.NoError(t, err)
		_, err = db.Collection("user_alert_relations").Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys: bson.D{{Key: "alert_id", Value: 1}}, Options: options.Index().SetName("alert_id_1").SetHidden(true),
		})
		require.NoError(t, err)
		monitor := telegramDao.NewMonitor(ssoMongoDB{db})
		logCtx, output := newIndexLogger(t, ctx)
		for range 2 {
			require.NoError(t, monitor.EnsureIndexes(logCtx))
		}
		require.NotContains(t, output.String(), `"msg":"create monitor index"`)
		indexes := indexesOn(t, ctx, db.Collection("alert_types"))
		require.Len(t, indexes, 2)
		require.Contains(t, indexes, "alert_name_unique")
		require.Contains(t, output.String(), `"problems":["hidden from the query planner"]`, "a hidden index is reported, not changed")
		require.Equal(t, true, indexesOn(t, ctx, db.Collection("user_alert_relations"))["alert_id_1"]["hidden"])
	})

	t.Run("an operator's index without uniqueness is kept", func(t *testing.T) {
		db := newAlertMongo(t, ctx)
		_, err := db.Collection("alert_types").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "name", Value: 1}}})
		require.NoError(t, err)
		monitor := telegramDao.NewMonitor(ssoMongoDB{db})
		logCtx, output := newIndexLogger(t, ctx)
		require.NoError(t, monitor.EnsureIndexes(logCtx))
		require.Contains(t, output.String(), `"problems":["not unique"]`)
		require.Nil(t, indexesOn(t, ctx, db.Collection("alert_types"))["name_1"]["unique"])
	})
}

// TestMongoTelegramServiceEnsuresMonitorIndexes covers the wiring: the telegram
// service makes sure of the indexes when it starts, and a start that cannot do
// so fails instead of serving unindexed lookups unnoticed.
func TestMongoTelegramServiceEnsuresMonitorIndexes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	prior := gconfig.Shared.Get("tasks")
	t.Cleanup(func() { gconfig.Shared.Set("tasks", prior) })
	gconfig.Shared.Set("tasks", []string{})
	db := newAlertMongo(t, ctx)
	endpoint := httptest.NewServer(&receiverBot{failures: map[string]string{}})
	t.Cleanup(endpoint.Close)
	monitor := telegramDao.NewMonitor(ssoMongoDB{db})

	ended, end := context.WithCancel(ctx)
	end()
	_, err := telegramSvc.New(ended, monitor, nil, nil, nil, receiverAlertToken, endpoint.URL)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "ensure monitor indexes")

	_, err = telegramSvc.New(ctx, monitor, nil, nil, nil, receiverAlertToken, endpoint.URL)
	require.NoError(t, err)
	require.Equal(t, true, indexesOn(t, ctx, db.Collection("alert_types"))["name_1"]["unique"])
	require.Contains(t, indexesOn(t, ctx, db.Collection("user_alert_relations")), "alert_id_1")
}
