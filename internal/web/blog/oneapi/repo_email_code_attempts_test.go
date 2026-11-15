package oneapi

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// emailCodeTestPostgresDSN optionally points the migration test at a
// disposable PostgreSQL database in addition to SQLite.
const emailCodeTestPostgresDSN = "ONEAPI_EMAIL_CODE_TEST_POSTGRES_DSN"

// legacyEmailCode is the sso_email_verification_codes row shape before
// verification attempts were tracked.
type legacyEmailCode struct {
	ID        string    `gorm:"type:char(36);column:id;primaryKey"`
	Account   string    `gorm:"column:account;uniqueIndex:idx_sso_evc_account_purpose;size:254"`
	Purpose   string    `gorm:"column:purpose;uniqueIndex:idx_sso_evc_account_purpose;size:32"`
	CodeHash  string    `gorm:"column:code_hash;size:64"`
	CreatedAt time.Time `gorm:"column:created_at"`
	ExpiresAt time.Time `gorm:"column:expires_at;index"`
}

// TableName returns the shared email-code table name.
func (legacyEmailCode) TableName() string { return "sso_email_verification_codes" }

// TestRepoEmailCodeAttemptReservationIsBounded proves concurrent reservations
// against one challenge succeed exactly maxAttempts times, that expired
// challenges cannot be reserved, and that a replacement code starts afresh.
func TestRepoEmailCodeAttemptReservationIsBounded(t *testing.T) {
	repo, _ := newTestRepo(t)
	now := time.Now().UTC()
	code := SSOEmailVerificationCode{ID: gutils.UUID7(), Account: "attempts@example.com", Purpose: "login",
		CodeHash: strings.Repeat("c", 64), CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	require.NoError(t, repo.ReplaceEmailCode(t.Context(), code))

	const maxAttempts = 5
	var reserved, refused atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := repo.ReserveEmailCodeAttempt(context.Background(), code.ID, maxAttempts, now)
			switch {
			case err == nil:
				reserved.Add(1)
			case errors.Is(err, ErrNotFound):
				refused.Add(1)
			default:
				t.Errorf("unexpected reservation error: %v", err)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, maxAttempts, reserved.Load())
	require.EqualValues(t, 50-maxAttempts, refused.Load())

	replacement := code
	replacement.ID = gutils.UUID7()
	require.NoError(t, repo.ReplaceEmailCode(t.Context(), replacement))
	require.NoError(t, repo.ReserveEmailCodeAttempt(t.Context(), replacement.ID, maxAttempts, now))
	require.ErrorIs(t, repo.ReserveEmailCodeAttempt(t.Context(), replacement.ID, maxAttempts, now.Add(2*time.Minute)), ErrNotFound,
		"an expired challenge has no attempts")
	require.ErrorIs(t, repo.ReserveEmailCodeAttempt(t.Context(), code.ID, maxAttempts, now), ErrNotFound,
		"a replaced challenge is gone")
}

// TestRepoPrepareKeepsExistingEmailCodesUsable proves Prepare adds the
// attempts column to a table created before it existed, with every stored
// code starting at zero attempts, so codes issued before an upgrade still
// verify afterwards and are then capped like new ones.
func TestRepoPrepareKeepsExistingEmailCodesUsable(t *testing.T) {
	drivers := map[string]Options{
		"sqlite": {Driver: "sqlite", SQLitePath: fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))},
	}
	if dsn := os.Getenv(emailCodeTestPostgresDSN); dsn != "" {
		drivers["postgres"] = Options{Driver: "postgres", DSN: dsn}
	}
	for name, opts := range drivers {
		t.Run(name, func(t *testing.T) {
			db, err := NewDB(t.Context(), opts)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, db.Migrator().DropTable(&SSOEmailVerificationCode{}))
				sqlDB, dbErr := db.DB()
				require.NoError(t, dbErr)
				require.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.Migrator().DropTable(&SSOEmailVerificationCode{}))
			require.NoError(t, db.AutoMigrate(&User{}, &oneAPIToken{}, &oneAPIOption{}, &PasskeyCredential{}, &legacyEmailCode{}))
			now := time.Now().UTC()
			legacy := legacyEmailCode{ID: gutils.UUID7(), Account: "upgrade@example.com", Purpose: "register",
				CodeHash: strings.Repeat("d", 64), CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
			require.NoError(t, db.Create(&legacy).Error)

			repo := New(log.Logger.Named("oneapi_test"), db)
			require.NoError(t, repo.Prepare(t.Context()))
			require.True(t, db.Migrator().HasColumn(&SSOEmailVerificationCode{}, "attempts"))

			stored, err := repo.FindValidEmailCode(t.Context(), legacy.Account, legacy.Purpose, now)
			require.NoError(t, err)
			require.Zero(t, stored.Attempts, "existing codes start with no attempts spent")
			for i := range 5 {
				require.NoError(t, repo.ReserveEmailCodeAttempt(t.Context(), legacy.ID, 5, now), "attempt %d", i+1)
			}
			require.ErrorIs(t, repo.ReserveEmailCodeAttempt(t.Context(), legacy.ID, 5, now), ErrNotFound)
			require.NoError(t, repo.ConsumeEmailCode(t.Context(), legacy.ID, legacy.CodeHash), "consume stays keyed by id and hash")
			require.ErrorIs(t, db.Where("id = ?", legacy.ID).First(&SSOEmailVerificationCode{}).Error, gorm.ErrRecordNotFound)
		})
	}
}
