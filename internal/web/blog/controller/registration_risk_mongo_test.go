package controller

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

const (
	registrationPassword    = "correct-horse-9"
	registrationDisplayName = "Newcomer"
)

// insertEmailCode stores a live email code for account and purpose, hashed the
// way the service hashes issued codes (HMAC-SHA256 of account, purpose and
// code under settings.secret), so a test can complete the flow without SMTP.
func (h *loginRiskHarness) insertEmailCode(t *testing.T, account string, purpose string, code string) {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(gconfig.Shared.GetString("settings.secret")))
	for i, part := range []string{account, purpose, code} {
		if i > 0 {
			mac.Write([]byte{0})
		}
		mac.Write([]byte(part))
	}
	now := time.Now().UTC()
	_, err := h.db.healthy.Collection("email_verification_codes").InsertOne(context.Background(), bson.M{
		"_id": primitive.NewObjectID(), "account": account, "purpose": purpose,
		"code_hash": hex.EncodeToString(mac.Sum(nil)), "created_at": now, "expires_at": now.Add(10 * time.Minute),
	})
	require.NoError(t, err)
}

// register submits a registration with the given code from clientIP.
func (h *loginRiskHarness) register(t *testing.T, clientIP string, account string, code string) error {
	t.Helper()
	_, err := h.resolver.UserRegister(requestContext(t, clientIP), account, registrationPassword, registrationDisplayName, "", nil, code)
	return err
}

// TestMongoRegistrationCodeAccountBudgetSurvivesIPRotation reproduces review
// P2 "registration verification codes bypass account-level failure tracking":
// wrong registration codes for one email from rotating addresses must
// escalate that email, and requesting a fresh code must not reset it.
func TestMongoRegistrationCodeAccountBudgetSurvivesIPRotation(t *testing.T) {
	h := newLoginRiskHarness(t)
	stubTurnstile(t, true)

	for i := range 3 {
		err := h.register(t, fmt.Sprintf("198.51.100.%d", 200+i), "newcomer@example.test", "123456")
		require.ErrorIs(t, err, model.ErrInvalidEmailVerificationCode, "guess %d is below the threshold", i+1)
	}

	err := h.register(t, "198.51.100.210", "Newcomer@Example.test", "654321")
	require.ErrorIs(t, err, model.ErrTurnstileRequired, "a fresh address must not get a fresh budget for the same email")
	_, err = h.resolver.UserRequestEmailCode(requestContext(t, "198.51.100.211"), "newcomer@example.test", "register", nil)
	require.ErrorIs(t, err, model.ErrTurnstileRequired, "a new code must not reset the account failure count")

	err = h.register(t, "198.51.100.212", "someone-else@example.test", "123456")
	require.ErrorIs(t, err, model.ErrInvalidEmailVerificationCode, "other emails are not challenged")
}

// TestMongoConcurrentRegistrationGuessesRespectAccountBudget proves 50
// concurrent registration guesses for one email from 50 addresses reach the
// code check at most the account threshold of times.
func TestMongoConcurrentRegistrationGuessesRespectAccountBudget(t *testing.T) {
	h := newLoginRiskHarness(t)
	stubTurnstile(t, true)

	const n = 50
	var checked, challenged atomic.Int32
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := h.register(t, fmt.Sprintf("198.51.101.%d", i), "burst-register@example.test", fmt.Sprintf("%06d", i))
			switch {
			case errors.Is(err, model.ErrInvalidEmailVerificationCode):
				checked.Add(1)
			case errors.Is(err, model.ErrTurnstileRequired):
				challenged.Add(1)
			default:
				t.Errorf("unexpected registration error: %v", err)
			}
		}()
	}
	wg.Wait()

	require.LessOrEqual(t, checked.Load(), int32(3), "concurrent guesses must not exceed the account threshold")
	require.EqualValues(t, n, checked.Load()+challenged.Load())
}

// TestMongoRegistrationInfrastructureFailureIsNotACodeFailure proves a
// database outage during registration neither reports a wrong code nor
// consumes the account budget, while genuine wrong codes still do.
func TestMongoRegistrationInfrastructureFailureIsNotACodeFailure(t *testing.T) {
	h := newLoginRiskHarness(t)
	stubTurnstile(t, true)

	h.db.down.Store(true)
	for i := range 5 {
		err := h.register(t, fmt.Sprintf("198.51.102.%d", i), "outage-register@example.test", "123456")
		require.Error(t, err)
		require.NotErrorIs(t, err, model.ErrInvalidEmailVerificationCode, "an outage is not a wrong code")
		require.NotErrorIs(t, err, model.ErrTurnstileRequired, "an outage must not escalate the account")
		require.Equal(t, model.ErrLoginUnavailable.Error(), err.Error(), "storage details never reach the client")
	}
	h.db.down.Store(false)

	for i := range 3 {
		err := h.register(t, fmt.Sprintf("198.51.102.%d", 10+i), "outage-register@example.test", "123456")
		require.ErrorIs(t, err, model.ErrInvalidEmailVerificationCode)
	}
	err := h.register(t, "198.51.102.20", "outage-register@example.test", "123456")
	require.ErrorIs(t, err, model.ErrTurnstileRequired)
}

// TestMongoRegistrationSucceedsAfterTyposAndResetsTheBudget is the control: a
// user who mistypes the code twice still registers with the right one, the
// code is single use, and the new account signs in without a challenge.
func TestMongoRegistrationSucceedsAfterTyposAndResetsTheBudget(t *testing.T) {
	h := newLoginRiskHarness(t)
	calls := stubTurnstile(t, true)
	const account = "typo-register@example.test"
	h.insertEmailCode(t, account, model.EmailVerificationPurposeRegister, "424242")

	for i := range 2 {
		require.ErrorIs(t, h.register(t, "198.51.103.1", account, fmt.Sprintf("00000%d", i)), model.ErrInvalidEmailVerificationCode)
	}
	require.NoError(t, h.register(t, "198.51.103.1", account, "424242"))
	require.ErrorIs(t, h.register(t, "198.51.103.2", account, "424242"), model.ErrInvalidEmailVerificationCode,
		"a registration code is single use")

	resp, err := h.resolver.UserLogin(requestContext(t, "198.51.103.3"), account, registrationPassword, nil, nil)
	require.NoError(t, err, "the new account signs in without a challenge")
	require.NotEmpty(t, resp.Token)
	require.Zero(t, calls.Load())
}

// TestMongoCollidingAccountSignInKeepsVictimChallenged reproduces review P2
// "account folding can group different MongoDB identities" end to end with
// two real MongoDB accounts whose spellings fold together: the owner of one
// signing in must not let guesses against the other skip the challenge.
func TestMongoCollidingAccountSignInKeepsVictimChallenged(t *testing.T) {
	h := newLoginRiskHarness(t)
	calls := stubTurnstile(t, true)
	ownerPassword := h.insertUser(t, "alice@example.test", nil)
	h.insertUser(t, "alicé@example.test", nil)

	for i := range 3 {
		_, err := h.resolver.UserLogin(requestContext(t, fmt.Sprintf("198.51.104.%d", i)), "alicé@example.test", "wrong", nil, nil)
		require.ErrorIs(t, err, model.ErrInvalidCredentials)
	}

	owner := requestContext(t, "192.0.2.80")
	_, err := h.resolver.UserLogin(owner, "alice@example.test", ownerPassword, nil, nil)
	require.ErrorIs(t, err, model.ErrTurnstileRequired, "the shared spelling is high risk, so the colliding owner is challenged too")
	token := "solved-turnstile-token"
	resp, err := h.resolver.UserLogin(owner, "alice@example.test", ownerPassword, &token, nil)
	require.NoError(t, err, "the colliding owner still signs in after one challenge")
	require.NotEmpty(t, resp.Token)
	require.EqualValues(t, 1, calls.Load())

	for i := range 5 {
		_, err = h.resolver.UserLogin(requestContext(t, fmt.Sprintf("198.51.104.%d", 50+i)), "alicé@example.test", "wrong-again", nil, nil)
		require.ErrorIs(t, err, model.ErrTurnstileRequired, "guess %d against the other account must still be challenged", i+1)
	}
}

// TestMongoCollidingAccountEmailCodeGuessesStayChallenged is the end-to-end
// form of TestAuthSuccessKeepsUnattributedFailuresOfCollidingSpelling: wrong
// email codes against one MongoDB account reveal no identity, and the owner
// of a colliding account signing in must not reopen guessing against it.
func TestMongoCollidingAccountEmailCodeGuessesStayChallenged(t *testing.T) {
	h := newLoginRiskHarness(t)
	stubTurnstile(t, true)
	ownerPassword := h.insertUser(t, "alice@example.test", nil)
	h.insertUser(t, "alicé@example.test", nil)

	for i := range 3 {
		_, err := h.resolver.UserLoginWithEmailCode(requestContext(t, fmt.Sprintf("198.51.105.%d", i)), "alicé@example.test", "123456", nil)
		require.ErrorIs(t, err, model.ErrInvalidCredentials)
	}
	token := "solved-turnstile-token"
	_, err := h.resolver.UserLogin(requestContext(t, "192.0.2.81"), "alice@example.test", ownerPassword, &token, nil)
	require.NoError(t, err)

	_, err = h.resolver.UserLoginWithEmailCode(requestContext(t, "198.51.105.50"), "alicé@example.test", "654321", nil)
	require.ErrorIs(t, err, model.ErrTurnstileRequired, "the colliding sign-in must not reset guessing against the other account")
}
