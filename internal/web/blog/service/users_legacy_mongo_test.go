package service

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v6"
	gcrypto "github.com/Laisky/go-utils/v6/crypto"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/dao"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// legacyLongPassword is longer than OneAPI's 20-character limit but within
// what the MongoDB store accepted before the OneAPI integration.
const legacyLongPassword = "correct-horse-battery-24"

// newLegacyMongoBlog builds the real service on an ephemeral MongoDB database.
func newLegacyMongoBlog(t *testing.T) (*Blog, *mongo.Database) {
	t.Helper()
	uri := os.Getenv("MONGO_QUERY_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_QUERY_TEST_URI is required; the Mongo query CI job always sets it")
	}
	previousSecret := gconfig.Shared.Get("settings.secret")
	gconfig.Shared.Set("settings.secret", "legacy-mongo-secret-for-tests-0123456789")
	t.Cleanup(func() { gconfig.Shared.Set("settings.secret", previousSecret) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	require.NoError(t, err)
	db := client.Database("legacy_users_" + primitive.NewObjectID().Hex())
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		require.NoError(t, db.Drop(cleanup))
		require.NoError(t, client.Disconnect(cleanup))
	})
	svc, err := New(ctx, glog.Shared, dao.New(glog.Shared, queryContractDB{db: db}, nil), nil)
	require.NoError(t, err)
	return svc, db
}

// insertLegacyAccount stores a pre-SSO shaped account (no status unless extra sets one).
func insertLegacyAccount(t *testing.T, db *mongo.Database, account string, extra bson.M) primitive.ObjectID {
	t.Helper()
	hash, err := gcrypto.PasswordHash([]byte(legacyLongPassword), gutils.HashTypeSha256)
	require.NoError(t, err)
	id := primitive.NewObjectID()
	doc := bson.M{"_id": id, "uid": gutils.UUID7(), "account": account, "username": "Legacy", "password": hash}
	for key, value := range extra {
		doc[key] = value
	}
	_, err = db.Collection("users").InsertOne(context.Background(), doc)
	require.NoError(t, err)
	return id
}

// insertLoginCode stores a valid login email code for account.
func insertLoginCode(t *testing.T, db *mongo.Database, account string, code string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := db.Collection("email_verification_codes").InsertOne(context.Background(), model.EmailVerificationCode{
		ID:        primitive.NewObjectID(),
		Account:   account,
		Purpose:   model.EmailVerificationPurposeLogin,
		CodeHash:  hashEmailVerificationCode(account, model.EmailVerificationPurposeLogin, code),
		CreatedAt: now,
		ExpiresAt: now.Add(time.Minute),
	})
	require.NoError(t, err)
}

// TestMongoLegacyAccountsCanUseEveryLoginMethod proves legacy MongoDB accounts
// without a status field can still sign in through email codes and GitHub, and
// can verify their long existing password when changing it or disabling TOTP.
func TestMongoLegacyAccountsCanUseEveryLoginMethod(t *testing.T) {
	svc, db := newLegacyMongoBlog(t)
	ctx := context.Background()

	t.Run("email code login", func(t *testing.T) {
		insertLegacyAccount(t, db, "email-code@example.test", nil)
		insertLoginCode(t, db, "email-code@example.test", "123456")
		user, err := svc.ValidateEmailCodeLogin(ctx, "email-code@example.test", "123456")
		require.NoError(t, err)
		require.Equal(t, "email-code@example.test", user.Account)
	})

	t.Run("email code is only sent to usable accounts", func(t *testing.T) {
		insertLegacyAccount(t, db, "eligible@example.test", nil)
		insertLegacyAccount(t, db, "pending-eligible@example.test", bson.M{"status": "pending"})
		ok, err := svc.shouldSendEmailVerificationCode(ctx, "eligible@example.test", model.EmailVerificationPurposeLogin)
		require.NoError(t, err)
		require.True(t, ok)
		ok, err = svc.shouldSendEmailVerificationCode(ctx, "pending-eligible@example.test", model.EmailVerificationPurposeLogin)
		require.NoError(t, err)
		require.False(t, ok)
	})

	t.Run("github login binds an existing legacy account", func(t *testing.T) {
		id := insertLegacyAccount(t, db, "github@example.test", nil)
		user, err := svc.GetOrCreateOIDCUser(ctx, "github", "1001", "github@example.test", "Zhonghua (Laisky) Cai, very long name")
		require.NoError(t, err)
		require.Equal(t, id, user.ID)

		again, err := svc.GetOrCreateOIDCUser(ctx, "github", "1001", "github@example.test", "Zhonghua (Laisky) Cai, very long name")
		require.NoError(t, err)
		require.Equal(t, id, again.ID)
	})

	t.Run("github login cannot revive a pending account", func(t *testing.T) {
		insertLegacyAccount(t, db, "github-pending@example.test", bson.M{"status": "pending"})
		_, err := svc.GetOrCreateOIDCUser(ctx, "github", "1002", "github-pending@example.test", "Pending")
		require.ErrorIs(t, err, model.ErrInvalidCredentials)
	})

	t.Run("change password verifies a long legacy password", func(t *testing.T) {
		id := insertLegacyAccount(t, db, "change@example.test", nil)
		user, err := svc.LoadUserByID(ctx, id)
		require.NoError(t, err)
		updated, err := svc.ChangePassword(ctx, user, legacyLongPassword, "new-password-1")
		require.NoError(t, err)
		require.NoError(t, gcrypto.VerifyHashedPassword([]byte("new-password-1"), updated.Password))
		_, err = svc.ChangePassword(ctx, updated, legacyLongPassword, "new-password-2")
		require.ErrorIs(t, err, model.ErrInvalidCredentials)
	})

	t.Run("disable totp verifies a long legacy password", func(t *testing.T) {
		id := insertLegacyAccount(t, db, "totp-off@example.test", bson.M{"totp_enabled": true, "totp_secret": newTOTPSecret()})
		user, err := svc.LoadUserByID(ctx, id)
		require.NoError(t, err)
		updated, err := svc.DisableTOTP(ctx, user, legacyLongPassword)
		require.NoError(t, err)
		require.False(t, updated.TOTPEnabled)
	})
}

// TestMongoEmailLoginCodeIsSingleUseUnderConcurrency proves concurrent
// submissions of one email code authenticate at most once.
func TestMongoEmailLoginCodeIsSingleUseUnderConcurrency(t *testing.T) {
	svc, db := newLegacyMongoBlog(t)
	insertLegacyAccount(t, db, "race@example.test", bson.M{"status": "active"})

	for round := range 5 {
		insertLoginCode(t, db, "race@example.test", "654321")
		const workers = 8
		var (
			wg        sync.WaitGroup
			mu        sync.Mutex
			successes int
		)
		start := make(chan struct{})
		for range workers {
			wg.Go(func() {
				<-start
				if _, err := svc.ValidateEmailCodeLogin(context.Background(), "race@example.test", "654321"); err == nil {
					mu.Lock()
					successes++
					mu.Unlock()
				}
			})
		}
		close(start)
		wg.Wait()
		require.Equal(t, 1, successes, "round %d: one email code must authenticate exactly once", round)
	}
}
