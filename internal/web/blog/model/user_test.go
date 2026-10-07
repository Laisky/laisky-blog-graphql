package model

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TestSyntheticObjectIDRoundTrip verifies OneAPI user IDs map to stable blog
// author identifiers without accepting legacy Mongo ObjectIDs.
func TestSyntheticObjectIDRoundTrip(t *testing.T) {
	objectID := SyntheticObjectID(42)
	require.False(t, objectID.IsZero())
	decoded, ok := OneAPIIDFromSyntheticObjectID(objectID)
	require.True(t, ok)
	require.Equal(t, 42, decoded)

	require.Equal(t, primitive.NilObjectID, SyntheticObjectID(0))
	_, ok = OneAPIIDFromSyntheticObjectID(primitive.NewObjectID())
	require.False(t, ok)
}

// TestUserIsActive pins the account-state contract shared by every login
// method: legacy MongoDB accounts without a status stay usable, while explicit
// inactive states and OneAPI users without an enabled status are rejected.
func TestUserIsActive(t *testing.T) {
	require.False(t, (*User)(nil).IsActive())
	require.True(t, (&User{Status: UserStatusActive}).IsActive())
	require.True(t, (&User{}).IsActive(), "pre-status MongoDB accounts must keep working")
	require.False(t, (&User{Status: UserStatusPending}).IsActive())
	require.False(t, (&User{Status: "disabled"}).IsActive())
	require.True(t, (&User{OneAPIID: 7, Status: UserStatusActive}).IsActive())
	require.False(t, (&User{OneAPIID: 7}).IsActive(), "OneAPI users always carry an explicit status")
	require.False(t, (&User{OneAPIID: 7, Status: UserStatusPending}).IsActive())
}
