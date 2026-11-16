package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/library"
)

// keyValidationUnavailableMsg answers a key the credential authority could
// not judge right now; the user stays in the waiting state and may resend it.
const keyValidationUnavailableMsg = "OneAPI API key validation is temporarily unavailable. Please try again shortly."

// invalidOneAPIKeyMsg answers a key the credential authority refused.
const invalidOneAPIKeyMsg = "Invalid OneAPI API key. Please double-check and try again." //nolint:gosec // G101: a user-facing message, not a credential

// authenticator returns the API key authenticator, resolved at call time:
// the injected one in tests, otherwise the process-wide installed one (nil
// before installation, which fails closed).
func (s *Telegram) authenticator() *mcpauth.Authenticator {
	if s != nil && s.keyAuthenticator != nil {
		return s.keyAuthenticator()
	}
	return mcpauth.Installed()
}

// validateOneAPIToken authenticates a OneAPI API key that a Telegram user
// typed, through the shared API key authenticator (internal/mcp/auth): the
// same one-api accept rule, verdict caches and length bound as every other
// entrypoint, and a one-api upstream budget charged to the Telegram user
// ("telegram:<uid>"), so one user's messages cannot flood one-api's per-IP
// limit. It returns the verified context, whose APIKeyHash is the canonical
// tenant hash (sha256 of the trimmed key) ask_user bindings are stored under.
// Errors match mcpauth's: ErrInvalidAuthorization, ErrCredentialRejected, or
// one for which mcpauth.IsUnavailable is true.
func (s *Telegram) validateOneAPIToken(ctx context.Context, telegramUID int64, key string) (*mcpauth.Context, error) {
	logger := gmw.GetLogger(ctx)
	ctx = mcpauth.WithBudgetSource(ctx, fmt.Sprintf("telegram:%d", telegramUID))
	verified, err := s.authenticator().Authenticate(ctx, "Bearer "+library.StripBearerPrefix(strings.TrimSpace(key)))
	if err != nil {
		logger.Debug("oneapi token refused", zap.Int64("uid", telegramUID), zap.Bool("unavailable", mcpauth.IsUnavailable(err)), zap.Error(err))
		return nil, errors.Wrap(err, "authenticate oneapi api key")
	}

	logger.Debug("oneapi token validated", zap.Int64("uid", telegramUID), zap.String("token_mask", verified.MaskedKey()))
	return verified, nil
}

// keyRefusalReply is the reply to a key validateOneAPIToken refused: a
// retry hint when no verdict was available, otherwise the invalid-key text.
func keyRefusalReply(err error) string {
	if mcpauth.IsUnavailable(err) {
		return keyValidationUnavailableMsg
	}
	return invalidOneAPIKeyMsg
}
