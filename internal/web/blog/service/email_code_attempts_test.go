package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v6"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"gorm.io/gorm"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	blogoneapi "github.com/Laisky/laisky-blog-graphql/internal/web/blog/oneapi"
)

const (
	attemptsTestCode = "424242"
	attemptsTestPass = "correct-horse-9"
)

// wrongCode returns a six-digit code that differs from attemptsTestCode.
func wrongCode(i int) string {
	return fmt.Sprintf("%06d", i)
}

// newOneAPIEmailCodeBlog builds a OneAPI-backed service whose database holds
// only the SSO-owned email-code table, enough to exercise code verification.
func newOneAPIEmailCodeBlog(t *testing.T) (*Blog, *blogoneapi.Repo, *gorm.DB) {
	t.Helper()
	previousSecret := gconfig.Shared.Get("settings.secret")
	gconfig.Shared.Set("settings.secret", "email-code-attempts-secret-0123456789")
	t.Cleanup(func() { gconfig.Shared.Set("settings.secret", previousSecret) })

	path := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := blogoneapi.NewDB(t.Context(), blogoneapi.Options{Driver: "sqlite", SQLitePath: path})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&blogoneapi.SSOEmailVerificationCode{}))
	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		require.NoError(t, dbErr)
		require.NoError(t, sqlDB.Close())
	})
	repo := blogoneapi.New(glog.Shared, db)
	return &Blog{logger: glog.Shared, oneapi: repo}, repo, db
}

// issueOneAPICode stores a live code for account and purpose.
func issueOneAPICode(t *testing.T, repo *blogoneapi.Repo, account string, purpose string) {
	t.Helper()
	now := gutils.Clock.GetUTCNow()
	require.NoError(t, repo.ReplaceEmailCode(t.Context(), blogoneapi.SSOEmailVerificationCode{
		ID: gutils.UUID7(), Account: account, Purpose: purpose,
		CodeHash:  hashEmailVerificationCode(account, purpose, attemptsTestCode),
		CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}))
}

// issueMongoCode stores a live code for account and purpose shaped like a
// document written before attempts were tracked (no attempts field).
func issueMongoCode(t *testing.T, db *mongo.Database, account string, purpose string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := db.Collection("email_verification_codes").InsertOne(context.Background(), bson.M{
		"_id": primitive.NewObjectID(), "account": account, "purpose": purpose,
		"code_hash":  hashEmailVerificationCode(account, purpose, attemptsTestCode),
		"created_at": now, "expires_at": now.Add(10 * time.Minute),
	})
	require.NoError(t, err)
}

// exhaustSequentially submits the per-code attempt cap of wrong codes.
func exhaustSequentially(t *testing.T, submit func(code string) error) {
	t.Helper()
	for i := range EmailVerificationCodeMaxAttempts {
		require.ErrorIs(t, submit(wrongCode(i)), model.ErrInvalidEmailVerificationCode, "wrong code %d", i+1)
	}
}

// exhaustConcurrently submits many wrong codes at once.
func exhaustConcurrently(t *testing.T, submit func(code string) error) {
	t.Helper()
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := submit(wrongCode(i))
			if !strings.Contains(fmt.Sprint(err), model.ErrInvalidEmailVerificationCode.Error()) {
				t.Errorf("wrong code %d: unexpected error %v", i, err)
			}
		}()
	}
	wg.Wait()
}

// TestOneAPIEmailCodeAttemptsAreCapped reproduces the root cause behind review
// P2 "registration verification codes bypass account-level failure tracking":
// a wrong guess left the code valid, so one emailed code accepted unlimited
// guesses for 30 minutes. The store now caps attempts per code, across clients
// and replicas, before the code is compared.
func TestOneAPIEmailCodeAttemptsAreCapped(t *testing.T) {
	for _, burst := range []struct {
		name    string
		exhaust func(*testing.T, func(string) error)
	}{{"sequential", exhaustSequentially}, {"concurrent", exhaustConcurrently}} {
		t.Run("login/"+burst.name, func(t *testing.T) {
			svc, repo, _ := newOneAPIEmailCodeBlog(t)
			const account = "login-cap@example.test"
			issueOneAPICode(t, repo, account, model.EmailVerificationPurposeLogin)
			submit := func(code string) error {
				return svc.ConsumeEmailVerificationCode(context.Background(), account, model.EmailVerificationPurposeLogin, code)
			}
			burst.exhaust(t, submit)
			require.ErrorIs(t, submit(attemptsTestCode), model.ErrInvalidEmailVerificationCode,
				"the right code must be refused once the code ran out of attempts")

			issueOneAPICode(t, repo, account, model.EmailVerificationPurposeLogin)
			require.NoError(t, submit(attemptsTestCode), "a newly issued code is usable again")
		})
		t.Run("register/"+burst.name, func(t *testing.T) {
			svc, repo, _ := newOneAPIEmailCodeBlog(t)
			const account = "register-cap@example.test"
			issueOneAPICode(t, repo, account, model.EmailVerificationPurposeRegister)
			submit := func(code string) error {
				_, err := svc.UserRegister(context.Background(), account, attemptsTestPass, "Capped", code)
				return err
			}
			burst.exhaust(t, submit)
			require.ErrorIs(t, submit(attemptsTestCode), model.ErrInvalidEmailVerificationCode,
				"the right code must be refused once the code ran out of attempts")
		})
	}
}

// TestOneAPIRegistrationCodeErrorsAreClassified proves registration reports a
// wrong or missing code as ErrInvalidEmailVerificationCode and a storage
// failure as something else, so only the former counts as a guess.
func TestOneAPIRegistrationCodeErrorsAreClassified(t *testing.T) {
	svc, _, db := newOneAPIEmailCodeBlog(t)
	_, err := svc.UserRegister(context.Background(), "missing@example.test", attemptsTestPass, "Missing", "123456")
	require.ErrorIs(t, err, model.ErrInvalidEmailVerificationCode)
	_, err = svc.UserRegister(context.Background(), "malformed@example.test", attemptsTestPass, "Malformed", "12ab56")
	require.ErrorIs(t, err, model.ErrInvalidEmailVerificationCode)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	_, err = svc.UserRegister(context.Background(), "outage@example.test", attemptsTestPass, "Outage", "123456")
	require.Error(t, err)
	require.NotErrorIs(t, err, model.ErrInvalidEmailVerificationCode, "a storage failure is not a wrong code")
	require.ErrorIs(t, err, model.ErrLoginUnavailable, "a storage failure is reported as unavailable")
}

// TestMongoOnlyTheNewestEmailCodeIsVerified reproduces the review finding
// that MongoDB may hold several live codes for one account (two requests
// racing through delete-then-insert) and verified an arbitrary one. Only the
// newest live code may be compared, so the attempt cap bounds comparisons per
// account and purpose, not per stored document.
func TestMongoOnlyTheNewestEmailCodeIsVerified(t *testing.T) {
	svc, db := newLegacyMongoBlog(t)
	const account = "two-codes@example.test"
	now := time.Now().UTC()
	insert := func(code string, createdAt time.Time) {
		_, err := db.Collection("email_verification_codes").InsertOne(context.Background(), bson.M{
			"_id": primitive.NewObjectID(), "account": account, "purpose": model.EmailVerificationPurposeLogin,
			"code_hash":  hashEmailVerificationCode(account, model.EmailVerificationPurposeLogin, code),
			"created_at": createdAt, "expires_at": now.Add(10 * time.Minute),
		})
		require.NoError(t, err)
	}
	insert("111111", now.Add(-time.Minute))
	insert("222222", now)
	submit := func(code string) error {
		return svc.ConsumeEmailVerificationCode(context.Background(), account, model.EmailVerificationPurposeLogin, code)
	}

	require.ErrorIs(t, submit("111111"), model.ErrInvalidEmailVerificationCode, "a superseded code is not accepted")
	for i := range EmailVerificationCodeMaxAttempts - 1 {
		require.ErrorIs(t, submit(wrongCode(i)), model.ErrInvalidEmailVerificationCode)
	}
	require.ErrorIs(t, submit("111111"), model.ErrInvalidEmailVerificationCode,
		"exhausting the newest code must not fall back to an older live one")
	require.ErrorIs(t, submit("222222"), model.ErrInvalidEmailVerificationCode, "the newest code is out of attempts")

	insert("333333", now.Add(time.Second))
	require.NoError(t, submit("333333"), "a newly issued code verifies on the first try")
}

// TestMongoEmailCodeAttemptsAreCapped is the MongoDB form of
// TestOneAPIEmailCodeAttemptsAreCapped. Its codes are shaped like documents
// written before attempts were tracked, so existing codes keep working.
func TestMongoEmailCodeAttemptsAreCapped(t *testing.T) {
	for _, burst := range []struct {
		name    string
		exhaust func(*testing.T, func(string) error)
	}{{"sequential", exhaustSequentially}, {"concurrent", exhaustConcurrently}} {
		t.Run("login/"+burst.name, func(t *testing.T) {
			svc, db := newLegacyMongoBlog(t)
			const account = "login-cap@example.test"
			issueMongoCode(t, db, account, model.EmailVerificationPurposeLogin)
			submit := func(code string) error {
				return svc.ConsumeEmailVerificationCode(context.Background(), account, model.EmailVerificationPurposeLogin, code)
			}
			burst.exhaust(t, submit)
			require.ErrorIs(t, submit(attemptsTestCode), model.ErrInvalidEmailVerificationCode,
				"the right code must be refused once the code ran out of attempts")
		})
		t.Run("register/"+burst.name, func(t *testing.T) {
			svc, db := newLegacyMongoBlog(t)
			const account = "register-cap@example.test"
			issueMongoCode(t, db, account, model.EmailVerificationPurposeRegister)
			submit := func(code string) error {
				_, err := svc.UserRegister(context.Background(), account, attemptsTestPass, "Capped", code)
				return err
			}
			burst.exhaust(t, submit)
			require.ErrorIs(t, submit(attemptsTestCode), model.ErrInvalidEmailVerificationCode,
				"the right code must be refused once the code ran out of attempts")
			n, err := db.Collection("users").CountDocuments(context.Background(), bson.M{"account": account})
			require.NoError(t, err)
			require.Zero(t, n, "no account is created from an exhausted code")
		})
	}
}

// TestMongoLegacyEmailCodeStillVerifies is the compatibility control: a code
// stored before attempts were tracked verifies on the first try, after a few
// typos, and only once.
func TestMongoLegacyEmailCodeStillVerifies(t *testing.T) {
	svc, db := newLegacyMongoBlog(t)
	const account = "legacy-code@example.test"
	issueMongoCode(t, db, account, model.EmailVerificationPurposeLogin)
	for i := range EmailVerificationCodeMaxAttempts - 1 {
		require.ErrorIs(t, svc.ConsumeEmailVerificationCode(context.Background(), account, model.EmailVerificationPurposeLogin, wrongCode(i)),
			model.ErrInvalidEmailVerificationCode)
	}
	require.NoError(t, svc.ConsumeEmailVerificationCode(context.Background(), account, model.EmailVerificationPurposeLogin, attemptsTestCode))
	require.ErrorIs(t, svc.ConsumeEmailVerificationCode(context.Background(), account, model.EmailVerificationPurposeLogin, attemptsTestCode),
		model.ErrInvalidEmailVerificationCode, "a code is single use")
}
