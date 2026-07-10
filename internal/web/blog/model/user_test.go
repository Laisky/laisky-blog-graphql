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
