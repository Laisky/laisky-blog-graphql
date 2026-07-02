package controller

import (
	"testing"

	gutils "github.com/Laisky/go-utils/v6"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// TestNewSSOProfileExposesExternalUID verifies the public profile uses the stable external UID.
func TestNewSSOProfileExposesExternalUID(t *testing.T) {
	userUID := gutils.UUID7()
	profile := newSSOProfile(&model.User{
		UID:      userUID,
		Account:  "alice@example.com",
		Username: "Alice",
	})

	require.Equal(t, userUID, profile.UID)
	_, err := uuid.Parse(profile.UID)
	require.NoError(t, err)
}
