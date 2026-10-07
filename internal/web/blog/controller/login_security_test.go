package controller

import (
	"context"
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

// TestCredentialFailureClassification verifies only credential rejections count
// toward the risk policy and that outages are reported distinctly.
func TestCredentialFailureClassification(t *testing.T) {
	clientErr, counts := credentialFailure(context.Background(), "password", errors.Wrap(model.ErrInvalidCredentials, "wrong password"))
	require.True(t, counts)
	require.ErrorIs(t, clientErr, model.ErrInvalidCredentials)
	require.Equal(t, model.ErrInvalidCredentials.Error(), clientErr.Error())

	clientErr, counts = credentialFailure(context.Background(), "password", errors.New("server selection timeout: 10.0.0.1:27017"))
	require.False(t, counts)
	require.ErrorIs(t, clientErr, model.ErrLoginUnavailable)
	require.Equal(t, model.ErrLoginUnavailable.Error(), clientErr.Error(), "internal details must not reach the client")
}

// TestTurnstileGateError verifies gate failures never claim the credentials were wrong.
func TestTurnstileGateError(t *testing.T) {
	require.ErrorIs(t, turnstileGateError(errors.Wrap(model.ErrTurnstileRequired, "login")), model.ErrTurnstileRequired)
	require.ErrorIs(t, turnstileGateError(errors.Wrap(model.ErrTurnstileFailed, "login")), model.ErrTurnstileFailed)
	require.ErrorIs(t, turnstileGateError(errors.New("unexpected")), model.ErrTurnstileFailed)
	require.NotErrorIs(t, turnstileGateError(errors.New("unexpected")), model.ErrInvalidCredentials)
}
