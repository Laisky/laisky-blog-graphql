package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/dao"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// TestPasswordCompatibilityWriters checks the actual Mongo update and insert,
// rather than a library round trip. Existing installations already write PHC.
func TestPasswordCompatibilityWriters(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("change password persists PHC", func(mt *mtest.T) {
		svc := &Blog{logger: glog.Shared, dao: dao.New(glog.Shared, queryContractDB{db: mt.DB}, nil)}
		user := &model.User{ID: primitive.NewObjectID(), Account: "compat@example.test", Password: passwordFixture(mt, "old-phc")}
		mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}))
		updated, err := svc.ChangePassword(context.Background(), user, "compat-old-password", "compat-new-password")
		require.NoError(mt, err)
		require.Contains(mt, updated.Password, "$argon2id$v=19$m=65536,t=3,p=4$")
		event := mt.GetStartedEvent()
		require.Equal(mt, "update", event.CommandName)
		item := event.Command.Lookup("updates").Array().Index(0).Value().Document()
		persisted := item.Lookup("u").Document().Lookup("$set").Document().Lookup("password").StringValue()
		require.Equal(mt, updated.Password, persisted)
		recordPasswordEvidence(mt, "new-phc", persisted)
		addPasswordLoginResponse(mt, "compat@example.test", persisted)
		_, err = svc.ValidateLogin(context.Background(), "compat@example.test", "compat-new-password")
		require.NoError(mt, err)
		addPasswordLoginResponse(mt, "compat@example.test", persisted)
		_, err = svc.ValidateLogin(context.Background(), "compat@example.test", "compat-old-password")
		require.ErrorIs(mt, err, model.ErrInvalidCredentials)
	})
	mt.Run("registration persists PHC", func(mt *mtest.T) {
		svc := &Blog{logger: glog.Shared, dao: dao.New(glog.Shared, queryContractDB{db: mt.DB}, nil)}
		account := "register-compat@example.test"
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, mt.DB.Name()+".email_verification_codes", mtest.FirstBatch,
				bson.D{{Key: "_id", Value: primitive.NewObjectID()}, {Key: "code_hash", Value: hashEmailVerificationCode(account, model.EmailVerificationPurposeRegister, "123456")}, {Key: "expires_at", Value: time.Now().Add(time.Minute)}}),
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}),
			mtest.CreateCursorResponse(0, mt.DB.Name()+".users", mtest.FirstBatch),
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}),
		)
		user, err := svc.UserRegister(context.Background(), account, "compat-new-password", "Compatibility", "123456")
		require.NoError(mt, err)
		require.Contains(mt, user.Password, "$argon2id$v=19$m=65536,t=3,p=4$")
		var persisted string
		for _, event := range mt.GetAllStartedEvents() {
			if event.CommandName == "insert" {
				persisted = event.Command.Lookup("documents").Array().Index(0).Value().Document().Lookup("password").StringValue()
			}
		}
		require.Equal(mt, user.Password, persisted)
	})
}

// TestPasswordCompatibilityReaders exercises the real service and DAO with
// immutable records from the old pin, updated consumer and legacy digest writer.
func TestPasswordCompatibilityReaders(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, fixture := range []struct{ name, password string }{
		{"old-phc", "compat-old-password"}, {"new-phc", "compat-new-password"}, {"legacy-sha256", "compat-old-password"},
	} {
		mt.Run(fixture.name, func(mt *mtest.T) {
			svc := &Blog{logger: glog.Shared, dao: dao.New(glog.Shared, queryContractDB{db: mt.DB}, nil)}
			record := passwordFixture(mt, fixture.name)
			addPasswordLoginResponse(mt, "compat@example.test", record)
			user, err := svc.ValidateLogin(context.Background(), "compat@example.test", fixture.password)
			require.NoError(mt, err)
			require.Equal(mt, record, user.Password, "login must not silently rewrite records")
			addPasswordLoginResponse(mt, "compat@example.test", record)
			user, err = svc.ValidateLogin(context.Background(), "compat@example.test", "wrong-password")
			require.ErrorIs(mt, err, model.ErrInvalidCredentials)
			require.Nil(mt, user)
		})
	}
	for _, record := range []string{"", "$argon2id$v=19$m=999999999,t=3,p=4$bad$bad", "sha256.0.bad.bad", "$2a$unsupported"} {
		mt.Run("malformed", func(mt *mtest.T) {
			svc := &Blog{logger: glog.Shared, dao: dao.New(glog.Shared, queryContractDB{db: mt.DB}, nil)}
			addPasswordLoginResponse(mt, "compat@example.test", record)
			user, err := svc.ValidateLogin(context.Background(), "compat@example.test", "compat-old-password")
			require.ErrorIs(mt, err, model.ErrInvalidCredentials)
			require.Nil(mt, user)
			require.NotContains(mt, err.Error(), "compat-old-password")
			if record != "" {
				require.NotContains(mt, err.Error(), record)
			}
		})
	}
	mt.Run("missing account", func(mt *mtest.T) {
		svc := &Blog{logger: glog.Shared, dao: dao.New(glog.Shared, queryContractDB{db: mt.DB}, nil)}
		mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+".users", mtest.FirstBatch))
		user, err := svc.ValidateLogin(context.Background(), "missing@example.test", "wrong-password")
		require.ErrorIs(mt, err, model.ErrInvalidCredentials)
		require.Nil(mt, user)
	})
	mt.Run("store failure is not invalid credentials", func(mt *mtest.T) {
		svc := &Blog{logger: glog.Shared, dao: dao.New(glog.Shared, queryContractDB{db: mt.DB}, nil)}
		mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 2, Message: "synthetic unavailable store"}))
		user, err := svc.ValidateLogin(context.Background(), "compat@example.test", "compat-old-password")
		require.Error(mt, err)
		require.NotErrorIs(mt, err, model.ErrInvalidCredentials)
		require.Nil(mt, user)
	})
}

// passwordFixture loads public synthetic golden records, never operational data.
func passwordFixture(t testing.TB, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "password_compatibility.json"))
	require.NoError(t, err)
	var records map[string]string
	require.NoError(t, json.Unmarshal(raw, &records))
	require.NotEmpty(t, records[name])
	return records[name]
}

// addPasswordLoginResponse supplies a persisted record at the Mongo boundary.
func addPasswordLoginResponse(mt *mtest.T, account, record string) {
	mt.Helper()
	mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+".users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: primitive.NewObjectID()}, {Key: "account", Value: account}, {Key: "password", Value: record}, {Key: "status", Value: "active"}}))
}

// recordPasswordEvidence optionally exports only the synthetic writer record
// for independent rollback acceptance; normal tests leave all files unchanged.
func recordPasswordEvidence(t testing.TB, name, record string) {
	t.Helper()
	path := os.Getenv("GRAPHQL_PASSWORD_RECORD_EVIDENCE")
	if path == "" {
		return
	}
	data, err := json.Marshal(map[string]string{name: record})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0600))
}
