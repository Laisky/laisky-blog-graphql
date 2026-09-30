package service

import (
	"context"
	"os"
	"testing"
	"time"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/dao"
)

// TestMongoLiteralFilterWireContract checks actual driver serialization, including
// adversarial values, against independently specified pre-refactor BSON filters.
func TestMongoLiteralFilterWireContract(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "ordinary", `{"$ne":null}`, `$where`, `a.b`, "雪", "\x00"} {
		for _, pair := range []struct {
			name   string
			typed  any
			legacy any
		}{
			{"post", postNameFilter{Name: value}, bson.M{"post_name": value}},
			{"account", userAccountFilter{Account: value}, bson.M{"account": value}},
			{"uid", userUIDFilter{UID: value}, bson.D{{Key: "uid", Value: value}}},
			{"verification", verificationIdentityFilter{Account: value, Purpose: value}, bson.M{"account": value, "purpose": value}},
		} {
			t.Run(pair.name+"/"+value, func(t *testing.T) {
				require.Equal(t, decodeFilter(t, pair.legacy), decodeFilter(t, pair.typed))
			})
		}
	}
	for _, id := range []primitive.ObjectID{{}, primitive.NewObjectID()} {
		require.Equal(t, decodeFilter(t, bson.D{{Key: "_id", Value: id}}), decodeFilter(t, documentIDFilter{ID: id}))
	}
}

// decodeFilter returns the driver's BSON representation, not a JSON approximation.
func decodeFilter(t *testing.T, filter any) bson.M {
	t.Helper()
	wire, err := bson.Marshal(filter)
	require.NoError(t, err)
	var decoded bson.M
	require.NoError(t, bson.Unmarshal(wire, &decoded))
	return decoded
}

// queryContractDB adapts only the test database to the application's DAO boundary.
type queryContractDB struct{ db *mongo.Database }

// GetCol returns a collection from the isolated test database.
func (d queryContractDB) GetCol(name string) *mongo.Collection { return d.db.Collection(name) }

// CurrentDB returns the database owned by this test.
func (d queryContractDB) CurrentDB() *mongo.Database { return d.db }

// DB returns a database using the same test client.
func (d queryContractDB) DB(name string) *mongo.Database { return d.db.Client().Database(name) }

// Close leaves client cleanup to the test that owns its lifetime.
func (d queryContractDB) Close(context.Context) error { return nil }

// TestMongoQueryIntegration exercises production read/mutation boundaries on an
// ephemeral database. CI supplies the URI explicitly; a configured DB must work.
func TestMongoQueryIntegration(t *testing.T) {
	uri := os.Getenv("MONGO_QUERY_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_QUERY_TEST_URI is required; the Mongo query CI job always sets it")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	require.NoError(t, err)
	db := client.Database("pr51_queries_" + primitive.NewObjectID().Hex())
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		dropErr := db.Drop(cleanup)
		disconnectErr := client.Disconnect(cleanup)
		require.NoError(t, dropErr)
		require.NoError(t, disconnectErr)
	})
	require.NoError(t, client.Ping(ctx, nil))
	s := &Blog{logger: glog.Shared, dao: dao.New(glog.Shared, queryContractDB{db: db}, nil)}
	postID, userID, categoryID := primitive.NewObjectID(), primitive.NewObjectID(), primitive.NewObjectID()
	const uid = "01999df0-0000-7000-8000-000000000001"
	_, err = db.Collection("posts").InsertOne(ctx, bson.M{"_id": postID, "post_name": "canary", "post_author": userID, "category": categoryID})
	require.NoError(t, err)
	_, err = db.Collection("users").InsertOne(ctx, bson.M{"_id": userID, "uid": uid, "account": "owner@example.test"})
	require.NoError(t, err)
	_, err = db.Collection("categories").InsertOne(ctx, bson.M{"_id": categoryID, "name": "target"})
	require.NoError(t, err)
	_, err = db.Collection("comments").InsertOne(ctx, bson.M{"post_id": postID, "is_approved": true})
	require.NoError(t, err)

	exists, err := s.IsNameExists(ctx, "canary")
	require.NoError(t, err)
	require.True(t, exists)
	count, err := s.BlogCommentCount(ctx, "canary")
	require.NoError(t, err)
	require.Equal(t, 1, count)
	user, err := s.LoadUserByID(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, userID, user.ID)
	user, err = s.LoadUserByUID(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, userID, user.ID)
	user, err = s.FindUserByAccount(ctx, "owner@example.test")
	require.NoError(t, err)
	require.Equal(t, userID, user.ID)
	category, err := s.LoadCategoryByID(ctx, categoryID)
	require.NoError(t, err)
	require.Equal(t, categoryID, category.ID)

	for _, hostile := range []string{`{"$ne":null}`, `{"$regex":".*"}`, `$where`, `canary"},"$or":[{}]`} {
		exists, err = s.IsNameExists(ctx, hostile)
		require.NoError(t, err)
		require.False(t, exists)
		_, err = s.BlogCommentCount(ctx, hostile)
		require.ErrorIs(t, err, mongo.ErrNoDocuments)
		_, err = s.BlogComments(ctx, hostile, nil, nil)
		require.ErrorIs(t, err, mongo.ErrNoDocuments)
		_, err = s.BlogCreateComment(ctx, hostile, "body", "writer", "writer@example.test", nil, nil)
		require.ErrorIs(t, err, mongo.ErrNoDocuments)
		_, err = s.UpdatePostCategory(ctx, hostile, "target")
		require.ErrorIs(t, err, mongo.ErrNoDocuments)
	}

	// The account/purpose deletion must retain the other account AND purpose.
	codes := db.Collection("email_verification_codes")
	_, err = codes.InsertMany(ctx, []any{
		bson.M{"account": "owner@example.test", "purpose": "register"},
		bson.M{"account": "other@example.test", "purpose": "register"},
		bson.M{"account": "owner@example.test", "purpose": "reset_password"},
	})
	require.NoError(t, err)
	deleted, err := codes.DeleteMany(ctx, verificationIdentityFilter{Account: `{"$ne":null}`, Purpose: "register"})
	require.NoError(t, err)
	require.Zero(t, deleted.DeletedCount)
	deleted, err = codes.DeleteMany(ctx, verificationIdentityFilter{Account: "owner@example.test", Purpose: "register"})
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted.DeletedCount)
	remaining, err := codes.CountDocuments(ctx, bson.D{})
	require.NoError(t, err)
	require.EqualValues(t, 2, remaining)
}
