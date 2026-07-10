package oneapi

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"gorm.io/gorm"

	blogmodel "github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

func newTestRepo(t *testing.T) (*Repo, *gorm.DB) {
	t.Helper()
	path := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := NewDB(t.Context(), Options{Driver: "sqlite", SQLitePath: path})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &oneAPIToken{}, &oneAPIOption{}, &PasskeyCredential{}))
	repo := New(log.Logger.Named("oneapi_test"), db)
	require.NoError(t, repo.Prepare(t.Context()))
	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		require.NoError(t, dbErr)
		require.NoError(t, sqlDB.Close())
	})
	return repo, db
}

// TestRepoCreateAndAuthenticateUser verifies OneAPI-compatible creation,
// identity mapping, password authentication, and default token creation.
func TestRepoCreateAndAuthenticateUser(t *testing.T) {
	repo, db := newTestRepo(t)
	user, err := repo.CreateUser(t.Context(), "alice@example.com", "correct-horse", "Alice")
	require.NoError(t, err)
	require.Positive(t, user.OneAPIID)
	require.Equal(t, "alice@example.com", user.OneAPIUsername)
	require.Equal(t, blogmodel.SyntheticObjectID(user.OneAPIID), user.ID)
	_, err = uuid.Parse(user.UID)
	require.NoError(t, err)
	require.True(t, repo.VerifyPassword(user, "correct-horse"))

	byEmail, err := repo.ValidateLogin(t.Context(), "alice@example.com", "correct-horse")
	require.NoError(t, err)
	require.Equal(t, user.UID, byEmail.UID)
	_, err = repo.ValidateLogin(t.Context(), "alice@example.com", "wrong-password")
	require.ErrorIs(t, err, blogmodel.ErrInvalidCredentials)

	var tokenCount int64
	require.NoError(t, db.Model(&oneAPIToken{}).Where("user_id = ?", user.OneAPIID).Count(&tokenCount).Error)
	require.EqualValues(t, 1, tokenCount)

	_, err = repo.CreateUser(t.Context(), "alice@example.com", "another-pass", "Alice 2")
	require.ErrorIs(t, err, blogmodel.ErrAccountExists)
}

// TestRepoRejectsDisabledUser verifies every password login rechecks OneAPI's
// current account status.
func TestRepoRejectsDisabledUser(t *testing.T) {
	repo, db := newTestRepo(t)
	user, err := repo.CreateUser(t.Context(), "disabled@example.com", "correct-horse", "Disabled")
	require.NoError(t, err)
	require.NoError(t, db.Model(&User{}).Where("id = ?", user.OneAPIID).Update("status", StatusDisabled).Error)
	_, err = repo.ValidateLogin(t.Context(), user.Account, "correct-horse")
	require.ErrorIs(t, err, blogmodel.ErrInvalidCredentials)
}

// TestRepoConcurrentIdentityLink verifies lazy identity mapping converges on a
// single stable public UID and blog ObjectID.
func TestRepoConcurrentIdentityLink(t *testing.T) {
	repo, db := newTestRepo(t)
	raw := User{UUID: gutils.UUID7(), Username: "concurrent", DisplayName: "Concurrent",
		Email: "concurrent@example.com", Status: StatusEnabled, Role: RoleCommonUser,
		AccessToken: strings.ReplaceAll(gutils.UUID7(), "-", ""), AffCode: "Ab12"}
	require.NoError(t, db.Create(&raw).Error)

	const workers = 12
	results := make(chan *blogmodel.User, workers)
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			user, err := repo.GetByID(context.Background(), raw.ID)
			results <- user
			errs <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var expectedUID string
	for user := range results {
		require.NotNil(t, user)
		if expectedUID == "" {
			expectedUID = user.UID
		}
		require.Equal(t, expectedUID, user.UID)
	}
	var linkCount int64
	require.NoError(t, db.Model(&SSOUserLink{}).Where("oneapi_user_id = ?", raw.ID).Count(&linkCount).Error)
	require.EqualValues(t, 1, linkCount)
}

// TestRepoImportLegacyUserLink verifies migration preserves an issued SSO UUID
// and historical blog author ObjectID idempotently.
func TestRepoImportLegacyUserLink(t *testing.T) {
	repo, db := newTestRepo(t)
	raw := User{UUID: gutils.UUID7(), Username: "legacy", DisplayName: "Legacy",
		Email: "legacy@example.com", Status: StatusEnabled, Role: RoleCommonUser,
		AccessToken: strings.ReplaceAll(gutils.UUID7(), "-", ""), AffCode: "Cd34"}
	require.NoError(t, db.Create(&raw).Error)
	legacyUID := gutils.UUID7()
	legacyObjectID := primitive.NewObjectID()
	require.NoError(t, repo.ImportLegacyUserLink(t.Context(), raw.ID, legacyUID, legacyObjectID))
	require.NoError(t, repo.ImportLegacyUserLink(t.Context(), raw.ID, legacyUID, legacyObjectID))
	user, err := repo.GetByUID(t.Context(), legacyUID)
	require.NoError(t, err)
	require.Equal(t, legacyUID, user.UID)
	require.Equal(t, legacyObjectID, user.ID)
	require.NotEqual(t, raw.UUID, user.UID)
	require.Error(t, repo.ImportLegacyUserLink(t.Context(), raw.ID, gutils.UUID7(), legacyObjectID))
}

// TestRepoEmailCodeIsSingleUse verifies concurrent consumers cannot replay an
// email verification challenge.
func TestRepoEmailCodeIsSingleUse(t *testing.T) {
	repo, _ := newTestRepo(t)
	now := time.Now().UTC()
	code := SSOEmailVerificationCode{ID: gutils.UUID7(), Account: "alice@example.com",
		Purpose: "login", CodeHash: strings.Repeat("a", 64), CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	require.NoError(t, repo.ReplaceEmailCode(t.Context(), code))
	loaded, err := repo.FindValidEmailCode(t.Context(), code.Account, code.Purpose, now)
	require.NoError(t, err)
	require.Equal(t, code.ID, loaded.ID)

	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- repo.ConsumeEmailCode(context.Background(), code.ID, code.CodeHash)
		}()
	}
	wait.Wait()
	close(results)
	successes := 0
	failures := 0
	for consumeErr := range results {
		if consumeErr == nil {
			successes++
		} else {
			require.True(t, errors.Is(consumeErr, ErrNotFound))
			failures++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, failures)
}

// TestRepoRegistrationRollsBackEmailCode verifies account creation failure does
// not consume the user's verified registration challenge.
func TestRepoRegistrationRollsBackEmailCode(t *testing.T) {
	repo, _ := newTestRepo(t)
	_, err := repo.CreateUser(t.Context(), "existing@example.com", "correct-horse", "Existing")
	require.NoError(t, err)
	now := time.Now().UTC()
	code := SSOEmailVerificationCode{ID: gutils.UUID7(), Account: "existing@example.com",
		Purpose: "register", CodeHash: strings.Repeat("b", 64), CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	require.NoError(t, repo.ReplaceEmailCode(t.Context(), code))
	_, err = repo.RegisterUserWithEmailCode(t.Context(), code.ID, code.CodeHash,
		code.Account, "another-pass", "Duplicate")
	require.ErrorIs(t, err, blogmodel.ErrAccountExists)
	_, err = repo.FindValidEmailCode(t.Context(), code.Account, code.Purpose, now)
	require.NoError(t, err)
}

// TestRepoOIDCBindingIsUnique verifies a GitHub subject cannot be bound to two
// OneAPI users.
func TestRepoOIDCBindingIsUnique(t *testing.T) {
	repo, _ := newTestRepo(t)
	first, err := repo.CreateUser(t.Context(), "first@example.com", "correct-horse", "First")
	require.NoError(t, err)
	second, err := repo.CreateUser(t.Context(), "second@example.com", "correct-horse", "Second")
	require.NoError(t, err)
	bound, err := repo.BindOIDCIdentity(t.Context(), first.OneAPIID, "github", "12345", first.Account)
	require.NoError(t, err)
	require.Len(t, bound.OIDCIdentities, 1)
	_, err = repo.BindOIDCIdentity(t.Context(), second.OneAPIID, "github", "12345", second.Account)
	require.Error(t, err)
	found, err := repo.FindByOIDCIdentity(t.Context(), "github", "12345")
	require.NoError(t, err)
	require.Equal(t, first.OneAPIID, found.OneAPIID)
}

// TestRepoPasskeyAndTOTP verifies native OneAPI passkey storage and pending
// TOTP promotion use the same authoritative user.
func TestRepoPasskeyAndTOTP(t *testing.T) {
	repo, _ := newTestRepo(t)
	user, err := repo.CreateUser(t.Context(), "secure@example.com", "correct-horse", "Secure")
	require.NoError(t, err)
	credential := &webauthn.Credential{
		ID:              []byte("credential-id"),
		PublicKey:       []byte("public-key"),
		AttestationType: "none",
		Transport:       []protocol.AuthenticatorTransport{protocol.USB},
		Flags:           webauthn.CredentialFlags{BackupEligible: true},
		Authenticator:   webauthn.Authenticator{AAGUID: []byte("aaguid"), SignCount: 4},
	}
	user, err = repo.AddPasskey(t.Context(), user, "Laptop", credential)
	require.NoError(t, err)
	require.Len(t, user.Passkeys, 1)
	owner, err := repo.FindByPasskeyCredentialID(t.Context(), credential.ID)
	require.NoError(t, err)
	require.Equal(t, user.OneAPIID, owner.OneAPIID)

	secret := "JBSWY3DPEHPK3PXP"
	require.NoError(t, repo.UpsertTOTPEnrollment(t.Context(), user.OneAPIID, secret, time.Now().UTC().Add(time.Minute)))
	enrollment, err := repo.GetTOTPEnrollment(t.Context(), user.OneAPIID, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, secret, enrollment.Secret)
	user, err = repo.ConfirmTOTPEnrollment(t.Context(), user.OneAPIID, secret)
	require.NoError(t, err)
	require.True(t, user.TOTPEnabled)
	user, err = repo.ClearTOTP(t.Context(), user.OneAPIID)
	require.NoError(t, err)
	require.False(t, user.TOTPEnabled)
}
