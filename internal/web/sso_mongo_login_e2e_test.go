package web

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/xlzd/gotp"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// legacyOwnerPassword is longer than OneAPI's 20-character limit but within the
// limits the Mongo store accepted before the OneAPI integration.
const legacyOwnerPassword = "correct-horse-battery-24"

// TestMongoSSOPasswordLoginLegacyAccount drives password login for existing
// MongoDB blog accounts through the real GraphQL route and proves the issued
// token is accepted by WhoAmI and UserProfile, which is what CV and the SSO
// login page require.
func TestMongoSSOPasswordLoginLegacyAccount(t *testing.T) {
	e := newSSOE2E(t)
	require.Len(t, legacyOwnerPassword, 24)

	t.Run("legacy account without status and with a long password", func(t *testing.T) {
		owner := e.insertLegacyUser(t, "owner@example.test", legacyOwnerPassword, true, nil)
		token := stringField(t, e.login(t, owner.Account, owner.Password, nil), "UserLogin", "token")

		require.Equal(t, owner.UID, stringField(t, e.whoAmI(t, token), "WhoAmI", "id"))
		require.Equal(t, owner.UID, stringField(t, e.graphql(t, token, ssoProfileQuery, nil), "UserProfile", "uid"))

		var stored bson.M
		require.NoError(t, e.db.Collection("users").FindOne(context.Background(), bson.M{"_id": owner.ID}).Decode(&stored))
		require.NotContains(t, stored, "status", "login must not migrate the legacy account document")
	})

	t.Run("legacy account without uid gets one stable uid", func(t *testing.T) {
		user := e.insertLegacyUser(t, "no-uid@example.test", legacyOwnerPassword, false, nil)
		token := stringField(t, e.login(t, user.Account, user.Password, nil), "UserLogin", "token")
		uid := stringField(t, e.whoAmI(t, token), "WhoAmI", "id")
		_, err := uuid.Parse(uid)
		require.NoError(t, err)

		again := stringField(t, e.login(t, user.Account, user.Password, nil), "UserLogin", "token")
		require.Equal(t, uid, stringField(t, e.whoAmI(t, again), "WhoAmI", "id"))
	})

	t.Run("wrong password is rejected", func(t *testing.T) {
		user := e.insertLegacyUser(t, "wrong-password@example.test", legacyOwnerPassword, true, nil)
		require.Contains(t, errorMessage(t, e.login(t, user.Account, legacyOwnerPassword+"x", nil)), "invalid credentials")
		require.Contains(t, errorMessage(t, e.login(t, "missing@example.test", legacyOwnerPassword, nil)), "invalid credentials")
	})

	t.Run("pending account is rejected before a token is issued", func(t *testing.T) {
		user := e.insertLegacyUser(t, "pending@example.test", legacyOwnerPassword, true, bson.M{"status": "pending"})
		response := e.login(t, user.Account, user.Password, nil)
		require.Contains(t, errorMessage(t, response), "invalid credentials")
	})

	t.Run("over-long input is still bounded", func(t *testing.T) {
		long := make([]byte, 101)
		for i := range long {
			long[i] = 'a'
		}
		require.Contains(t, errorMessage(t, e.login(t, "owner@example.test", string(long), nil)), "input too long")
	})

	t.Run("totp second step", func(t *testing.T) {
		secret := gotp.RandomSecret(20)
		user := e.insertLegacyUser(t, "totp@example.test", legacyOwnerPassword, true,
			bson.M{"totp_enabled": true, "totp_secret": secret})

		require.Contains(t, errorMessage(t, e.login(t, user.Account, user.Password, nil)), "totp_required")
		bad := "000000"
		if gotp.NewDefaultTOTP(secret).Now() == bad {
			bad = "111111"
		}
		require.Contains(t, errorMessage(t, e.login(t, user.Account, user.Password, &bad)), "invalid credentials")
		good := gotp.NewDefaultTOTP(secret).Now()
		token := stringField(t, e.login(t, user.Account, user.Password, &good), "UserLogin", "token")
		require.Equal(t, user.UID, stringField(t, e.whoAmI(t, token), "WhoAmI", "id"))
	})
}

// TestMongoSSOPasskeyLoginHandles proves discoverable passkey login for both the
// legacy ObjectID user handle (registrations before the UID switch) and the
// current UUID handle, together with the negative controls that must keep
// failing. Every negative control runs against both handle kinds, so each one
// differs from a passing positive control in exactly one dimension.
func TestMongoSSOPasskeyLoginHandles(t *testing.T) {
	e := newSSOE2E(t)

	owner := e.insertLegacyUser(t, "passkey-owner@example.test", legacyOwnerPassword, true, nil)
	other := e.insertLegacyUser(t, "passkey-other@example.test", legacyOwnerPassword, true, nil)
	cases := []struct {
		name   string
		key    *virtualPasskey
		handle []byte
	}{
		{name: "legacy ObjectID handle", key: newVirtualPasskey(t), handle: []byte(owner.ID.Hex())},
		{name: "current UUID handle", key: newVirtualPasskey(t), handle: []byte(owner.UID)},
	}
	for _, tc := range cases {
		e.addPasskey(t, owner.ID, tc.key.storedPasskey(t))
	}

	reject := func(t *testing.T, response graphQLResponse) {
		t.Helper()
		require.Regexp(t, `login failed|invalid credentials`, errorMessage(t, response))
		raw := response.Data["UserFinishPasskeyLogin"]
		require.True(t, len(raw) == 0 || string(raw) == "null", "a rejected ceremony must not issue a token: %s", string(raw))
	}

	for _, tc := range cases {
		key, handle := tc.key, tc.handle
		t.Run(tc.name, func(t *testing.T) {
			t.Run("succeeds and reaches WhoAmI", func(t *testing.T) {
				challenge, session := e.startPasskeyLogin(t)
				key.counter++
				response := e.finishPasskeyLogin(t, session, key.assertion(t, challenge, ssoE2EOrigin, handle, key.counter))
				token := stringField(t, response, "UserFinishPasskeyLogin", "token")
				require.Equal(t, owner.UID, stringField(t, e.whoAmI(t, token), "WhoAmI", "id"))
			})
			t.Run("another user's handle is rejected", func(t *testing.T) {
				for _, foreign := range [][]byte{[]byte(other.UID), []byte(other.ID.Hex()), []byte(primitive.NewObjectID().Hex())} {
					challenge, session := e.startPasskeyLogin(t)
					reject(t, e.finishPasskeyLogin(t, session, key.assertion(t, challenge, ssoE2EOrigin, foreign, key.counter+1)))
				}
			})
			t.Run("tampered signature is rejected", func(t *testing.T) {
				challenge, session := e.startPasskeyLogin(t)
				forged := newVirtualPasskey(t)
				forged.credentialID = key.credentialID
				reject(t, e.finishPasskeyLogin(t, session, forged.assertion(t, challenge, ssoE2EOrigin, handle, key.counter+1)))
			})
			t.Run("foreign origin is rejected", func(t *testing.T) {
				challenge, session := e.startPasskeyLogin(t)
				reject(t, e.finishPasskeyLogin(t, session, key.assertion(t, challenge, "https://evil.example.test", handle, key.counter+1)))
			})
			t.Run("challenge from another ceremony is rejected", func(t *testing.T) {
				otherChallenge, _ := e.startPasskeyLogin(t)
				_, session := e.startPasskeyLogin(t)
				reject(t, e.finishPasskeyLogin(t, session, key.assertion(t, otherChallenge, ssoE2EOrigin, handle, key.counter+1)))
			})
			t.Run("replayed assertion is rejected", func(t *testing.T) {
				challenge, session := e.startPasskeyLogin(t)
				key.counter++
				assertion := key.assertion(t, challenge, ssoE2EOrigin, handle, key.counter)
				stringField(t, e.finishPasskeyLogin(t, session, assertion), "UserFinishPasskeyLogin", "token")
				reject(t, e.finishPasskeyLogin(t, session, assertion))
			})
			t.Run("regressed signature counter is rejected", func(t *testing.T) {
				require.Greater(t, key.counter, uint32(1))
				challenge, session := e.startPasskeyLogin(t)
				reject(t, e.finishPasskeyLogin(t, session, key.assertion(t, challenge, ssoE2EOrigin, handle, 1)))
			})
		})
	}

	// Synced platform passkeys (iCloud Keychain, Google Password Manager) always
	// report a zero signature counter, so the counter cannot detect replay;
	// only the single-use ceremony session does.
	t.Run("zero-counter passkey", func(t *testing.T) {
		key := newVirtualPasskey(t)
		e.addPasskey(t, owner.ID, key.storedPasskey(t))
		for range 2 {
			challenge, session := e.startPasskeyLogin(t)
			response := e.finishPasskeyLogin(t, session, key.assertion(t, challenge, ssoE2EOrigin, []byte(owner.UID), 0))
			token := stringField(t, response, "UserFinishPasskeyLogin", "token")
			require.Equal(t, owner.UID, stringField(t, e.whoAmI(t, token), "WhoAmI", "id"))
		}

		challenge, session := e.startPasskeyLogin(t)
		assertion := key.assertion(t, challenge, ssoE2EOrigin, []byte(owner.UID), 0)
		stringField(t, e.finishPasskeyLogin(t, session, assertion), "UserFinishPasskeyLogin", "token")
		reject(t, e.finishPasskeyLogin(t, session, assertion))
	})

	// Only a verified assertion consumes a session, so anonymous callers cannot
	// exhaust the single-use registry with junk submissions.
	t.Run("a rejected assertion does not consume the session", func(t *testing.T) {
		key := newVirtualPasskey(t)
		e.addPasskey(t, owner.ID, key.storedPasskey(t))
		challenge, session := e.startPasskeyLogin(t)
		reject(t, e.finishPasskeyLogin(t, session, key.assertion(t, challenge, "https://evil.example.test", []byte(owner.UID), 0)))
		response := e.finishPasskeyLogin(t, session, key.assertion(t, challenge, ssoE2EOrigin, []byte(owner.UID), 0))
		require.Equal(t, owner.UID, stringField(t, e.whoAmI(t, stringField(t, response, "UserFinishPasskeyLogin", "token")), "WhoAmI", "id"))
	})

	t.Run("pending owner cannot use a passkey", func(t *testing.T) {
		pending := e.insertLegacyUser(t, "passkey-pending@example.test", legacyOwnerPassword, true, bson.M{"status": "pending"})
		key := newVirtualPasskey(t)
		e.addPasskey(t, pending.ID, key.storedPasskey(t))
		challenge, session := e.startPasskeyLogin(t)
		reject(t, e.finishPasskeyLogin(t, session, key.assertion(t, challenge, ssoE2EOrigin, []byte(pending.UID), 1)))
	})
}

// TestMongoSSOTokenRejectsInactiveAccount proves an already issued token stops
// working once the account is explicitly deactivated.
func TestMongoSSOTokenRejectsInactiveAccount(t *testing.T) {
	e := newSSOE2E(t)
	user := e.insertLegacyUser(t, "deactivated@example.test", legacyOwnerPassword, true, nil)
	token := stringField(t, e.login(t, user.Account, user.Password, nil), "UserLogin", "token")
	require.Equal(t, user.UID, stringField(t, e.whoAmI(t, token), "WhoAmI", "id"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := e.db.Collection("users").UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{"$set": bson.M{"status": "pending"}})
	require.NoError(t, err)
	require.Contains(t, errorMessage(t, e.whoAmI(t, token)), "invalid credentials")
}
