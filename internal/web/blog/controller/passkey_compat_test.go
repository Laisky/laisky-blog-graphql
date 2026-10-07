package controller

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// TestAcceptedPasskeyUserHandle verifies only the credential owner's current
// UID or legacy ObjectID handle is accepted, and nothing else is reflected.
func TestAcceptedPasskeyUserHandle(t *testing.T) {
	owner := &model.User{ID: primitive.NewObjectID(), UID: "01999df0-0000-7000-8000-000000000001"}

	require.Equal(t, []byte(owner.UID), acceptedPasskeyUserHandle(owner, []byte(owner.UID)))
	require.Equal(t, []byte(owner.ID.Hex()), acceptedPasskeyUserHandle(owner, []byte(owner.ID.Hex())))
	for _, foreign := range [][]byte{nil, []byte("attacker"), []byte(primitive.NewObjectID().Hex()), []byte("01999df0-0000-7000-8000-000000000002")} {
		require.Equal(t, []byte(owner.UID), acceptedPasskeyUserHandle(owner, foreign))
	}

	// A legacy account that has not been assigned a UID yet still matches its ObjectID handle.
	noUID := &model.User{ID: primitive.NewObjectID()}
	require.Equal(t, []byte(noUID.ID.Hex()), acceptedPasskeyUserHandle(noUID, []byte(noUID.ID.Hex())))
	require.Empty(t, acceptedPasskeyUserHandle(noUID, []byte("anything")))

	// OneAPI-native users never had ObjectID handles; their synthetic ID is not accepted.
	native := &model.User{ID: model.SyntheticObjectID(42), OneAPIID: 42, UID: "01999df0-0000-7000-8000-000000000003"}
	require.Nil(t, legacyPasskeyUserHandle(native))
	require.Equal(t, []byte(native.UID), acceptedPasskeyUserHandle(native, []byte(native.ID.Hex())))
}

// TestPasskeySessionRegistrySingleUse verifies a login session can be claimed
// once until it expires and that a full registry fails closed.
func TestPasskeySessionRegistrySingleUse(t *testing.T) {
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	registry := newPasskeySessionRegistry(2)
	registry.now = func() time.Time { return now }

	require.NoError(t, registry.claim("challenge-a", now.Add(time.Minute)))
	require.Error(t, registry.claim("challenge-a", now.Add(time.Minute)))
	require.NoError(t, registry.claim("challenge-b", now.Add(time.Minute)))
	require.Error(t, registry.claim("challenge-c", now.Add(time.Minute)), "a full registry must not evict live claims")
	require.Error(t, registry.claim("", now.Add(time.Minute)))

	now = now.Add(2 * time.Minute)
	require.NoError(t, registry.claim("challenge-c", now.Add(time.Minute)), "expired claims are pruned")
}

// TestDecodeStoredPasskeyCredentialClearsCloneWarning verifies a clone warning
// persisted by an earlier ceremony cannot lock the credential out forever.
func TestDecodeStoredPasskeyCredentialClearsCloneWarning(t *testing.T) {
	raw, err := json.Marshal(webauthn.Credential{
		ID:            []byte("credential"),
		PublicKey:     []byte("key"),
		Authenticator: webauthn.Authenticator{SignCount: 5, CloneWarning: true},
	})
	require.NoError(t, err)

	credential, err := decodeStoredPasskeyCredential(model.PasskeyCredential{CredentialJSON: string(raw)})
	require.NoError(t, err)
	require.False(t, credential.Authenticator.CloneWarning)
	require.EqualValues(t, 5, credential.Authenticator.SignCount)
}
