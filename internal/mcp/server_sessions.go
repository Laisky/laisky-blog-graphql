package mcp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	errors "github.com/Laisky/errors/v2"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	srv "github.com/mark3labs/mcp-go/server"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
)

// requestSessionIDResolver binds legacy transport sessions to the API key on
// each request. It keeps no shared identity cache and works across replicas.
// Modern stateless requests bypass legacy session management in the SDK.
type requestSessionIDResolver struct{}

var _ srv.SessionIdManagerResolver = requestSessionIDResolver{}

// ResolveSessionIdManager selects public or credential-bound session validation.
// API-key parsing identifies a namespace; tool authorization/billing still runs
// independently. A session ID never authenticates a missing or invalid API key.
func (requestSessionIDResolver) ResolveSessionIdManager(r *http.Request) srv.SessionIdManager {
	header, _ := resolveRequestAuthorizationHeader(r)
	auth, err := mcpauth.ParseAuthorizationContext(header)
	if err != nil {
		return &publicSessionIDManager{}
	}
	return &credentialSessionIDManager{key: sha256.Sum256([]byte(auth.APIKey))}
}

// publicSessionIDManager retains anonymous legacy discovery without allowing
// an unauthenticated DELETE to clean up a credential-bound transport session.
type publicSessionIDManager struct {
	srv.StatelessGeneratingSessionIdManager
}

// Terminate validates even anonymous session IDs before allowing SDK cleanup.
func (s *publicSessionIDManager) Terminate(sessionID string) (bool, error) {
	if _, err := s.Validate(sessionID); err != nil {
		return false, errors.New("invalid public MCP session")
	}
	return false, nil
}

// credentialSessionIDManager authenticates an opaque session nonce with a
// domain-separated HMAC. Only a digest of the current request's API key is held;
// neither the key nor its digest is serialized into the session ID.
// As with the SDK's previous stateless-generating manager, deletion cleans up
// transport state but is not durable token revocation.
type credentialSessionIDManager struct {
	key [sha256.Size]byte
}

var _ srv.SessionIdManager = (*credentialSessionIDManager)(nil)

const sessionMACDomain = "laisky-blog-graphql/mcp-session/v1\x00"

// Generate returns a random SDK nonce plus a credential-bound authenticator.
func (s *credentialSessionIDManager) Generate() string {
	nonce := (&srv.StatelessGeneratingSessionIdManager{}).Generate()
	return nonce + "." + hex.EncodeToString(s.signature(nonce))
}

// Validate rejects unsigned, malformed, tampered and other-caller session IDs.
func (s *credentialSessionIDManager) Validate(sessionID string) (bool, error) {
	nonce, signature, ok := strings.Cut(sessionID, ".")
	if !ok || len(signature) != 2*sha256.Size {
		return false, errors.New("invalid MCP session; initialize a new session with the current credential")
	}
	if _, err := (&srv.StatelessGeneratingSessionIdManager{}).Validate(nonce); err != nil {
		return false, errors.New("invalid MCP session nonce")
	}
	decoded, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(decoded, s.signature(nonce)) {
		return false, errors.New("MCP session does not match the current credential")
	}
	return false, nil
}

// Terminate permits SDK transport cleanup only for the credential owning the ID.
func (s *credentialSessionIDManager) Terminate(sessionID string) (bool, error) {
	if _, err := s.Validate(sessionID); err != nil {
		return false, err
	}
	return false, nil
}

// signature returns a constant-size, domain-separated authenticator for a nonce.
func (s *credentialSessionIDManager) signature(nonce string) []byte {
	mac := hmac.New(sha256.New, s.key[:])
	_, _ = mac.Write([]byte(sessionMACDomain))
	_, _ = mac.Write([]byte(nonce))
	return mac.Sum(nil)
}

// withLegacySessionOwnership checks non-POST session ownership before the SDK.
// The SDK validates POST sessions through the resolver, but GET can reuse cached
// sessions without Validate and DELETE maps Terminate errors to HTTP 500.
func withLegacySessionOwnership(next http.Handler) http.Handler {
	if next == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version := r.Header.Get(mcpgo.HeaderProtocolVersion)
		sessionID := r.Header.Get(mcpgo.HeaderSessionID)
		if (r.Method != http.MethodGet && r.Method != http.MethodDelete) ||
			mcpgo.IsModernProtocol(version) || sessionID == "" {
			// Preserve the SDK's method/version errors and sessionless behavior.
			next.ServeHTTP(w, r)
			return
		}

		manager := (requestSessionIDResolver{}).ResolveSessionIdManager(r)
		terminated, err := manager.Validate(sessionID)
		if err != nil || terminated {
			http.Error(w, "Invalid MCP session; initialize a new session with the current credential", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
