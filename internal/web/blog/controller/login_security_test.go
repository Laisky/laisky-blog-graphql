package controller

import (
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// TestMaskLoginErrorInvalidCredentials ensures invalid credentials are preserved as a safe error message.
func TestMaskLoginErrorInvalidCredentials(t *testing.T) {
	err := maskLoginError(model.ErrInvalidCredentials)
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrInvalidCredentials))
	require.Equal(t, model.ErrInvalidCredentials.Error(), err.Error())
}

// TestMaskLoginErrorInternal ensures internal errors are masked from the client.
func TestMaskLoginErrorInternal(t *testing.T) {
	err := maskLoginError(errors.New("db down"))
	require.Error(t, err)
	require.False(t, errors.Is(err, model.ErrInvalidCredentials))
	require.Equal(t, loginFailedMessage, err.Error())
}

// TestMaskLoginErrorNil ensures nil errors remain nil.
func TestMaskLoginErrorNil(t *testing.T) {
	require.NoError(t, maskLoginError(nil))
}
