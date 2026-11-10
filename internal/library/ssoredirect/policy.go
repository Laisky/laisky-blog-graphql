package ssoredirect

import (
	"encoding/base64"
	"fmt"
	"slices"
	"strings"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
)

const (
	// ModeAllow keeps the legacy bearer handoff for unregistered targets that pass the policy.
	ModeAllow = "allow"
	// ModeDeny refuses every unregistered target, completing the cutover to the code flow.
	ModeDeny = "deny"

	// ConfigKeyLegacyTokenRedirect selects the legacy bearer handoff mode; the default is allow.
	ConfigKeyLegacyTokenRedirect = "settings.web.sso.legacy_token_redirect"
	// ConfigKeyClients holds the registered code-flow clients, keyed by client ID.
	ConfigKeyClients = "settings.web.sso.clients"

	maxClients      = 32
	maxRedirectURIs = 8
	maxClientIDLen  = 64
)

// Client is one registered SSO client. A registered client receives only a single-use
// code for one of its exact redirect URIs, never the reusable bearer.
type Client struct {
	// ID is the client identifier sent as sso_client_id and client_id.
	ID string `json:"client_id"`
	// RedirectURIs are the exact callback URIs, compared byte for byte.
	RedirectURIs []string `json:"redirect_uris"`
	// LegacyTokenRedirect is deny by default. allow is a migration aid that lets the client's
	// hostname keep the legacy bearer handoff while its consumer ships code-flow support.
	LegacyTokenRedirect string `json:"legacy_token_redirect"`
}

// Policy is the issuer's redirect policy: the legacy handoff mode plus the client registry.
type Policy struct {
	// LegacyTokenRedirect is ModeAllow or ModeDeny.
	LegacyTokenRedirect string `json:"legacy_token_redirect"`
	// Clients are the registered code-flow clients, sorted by ID.
	Clients []Client `json:"clients"`
}

// FailClosed is the policy used when the configuration cannot be read: no registered
// clients beyond the built-in Blog client, and no legacy bearer handoff.
func FailClosed() Policy {
	return Policy{LegacyTokenRedirect: ModeDeny, Clients: []Client{}}
}

// Load reads and validates the policy from the shared configuration. On error it returns
// FailClosed together with the error, so callers that only log it still fail closed.
func Load() (Policy, error) {
	policy, err := Parse(gconfig.Shared.Get(ConfigKeyLegacyTokenRedirect), gconfig.Shared.Get(ConfigKeyClients))
	if err != nil {
		return FailClosed(), errors.Wrap(err, "load sso redirect policy")
	}
	return policy, nil
}

// Parse validates raw configuration values for the legacy mode and the client registry.
func Parse(rawMode, rawClients any) (Policy, error) {
	mode, err := parseMode(rawMode, ModeAllow)
	if err != nil {
		return FailClosed(), errors.Wrap(err, ConfigKeyLegacyTokenRedirect)
	}
	clients, err := parseClients(rawClients)
	if err != nil {
		return FailClosed(), errors.Wrap(err, ConfigKeyClients)
	}
	return Policy{LegacyTokenRedirect: mode, Clients: clients}, nil
}

// parseMode accepts an absent value (fallback) or one of allow and deny.
func parseMode(raw any, fallback string) (string, error) {
	if raw == nil {
		return fallback, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", errors.Errorf("must be %q or %q", ModeAllow, ModeDeny)
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return fallback, nil
	case ModeAllow:
		return ModeAllow, nil
	case ModeDeny:
		return ModeDeny, nil
	default:
		return "", errors.Errorf("must be %q or %q", ModeAllow, ModeDeny)
	}
}

// parseClients validates the registry map keyed by client ID.
func parseClients(raw any) ([]Client, error) {
	if raw == nil {
		return []Client{}, nil
	}
	entries, ok := stringMap(raw)
	if !ok {
		return nil, errors.New("must be a map keyed by client id")
	}
	if len(entries) > maxClients {
		return nil, errors.Errorf("at most %d clients are allowed", maxClients)
	}
	clients := make([]Client, 0, len(entries))
	for id, value := range entries {
		client, err := parseClient(id, value)
		if err != nil {
			return nil, errors.Wrapf(err, "client %q", id)
		}
		clients = append(clients, client)
	}
	slices.SortFunc(clients, func(a, b Client) int { return strings.Compare(a.ID, b.ID) })
	// A registration reserves its hostname, so one hostname belongs to exactly one client and
	// classification never depends on the order in which clients are examined.
	owners := make(map[string]string)
	for _, client := range clients {
		for _, uri := range client.RedirectURIs {
			parsed, _ := splitTarget(uri)
			if owner, taken := owners[parsed.host]; taken && owner != client.ID {
				return nil, errors.Errorf("hostname %q is registered by both %q and %q", parsed.host, owner, client.ID)
			}
			owners[parsed.host] = client.ID
		}
	}
	return clients, nil
}

// parseClient validates one registration.
func parseClient(id string, raw any) (Client, error) {
	if !validClientID(id) || id == BlogClientID {
		return Client{}, errors.New("client id must match ^[a-z][a-z0-9_-]{1,63}$ and must not be the built-in blog client")
	}
	fields, ok := stringMap(raw)
	if !ok {
		return Client{}, errors.New("must be a map")
	}
	for field := range fields {
		if field != "redirect_uris" && field != "legacy_token_redirect" {
			return Client{}, errors.Errorf("unknown field %q", field)
		}
	}
	mode, err := parseMode(fields["legacy_token_redirect"], ModeDeny)
	if err != nil {
		return Client{}, errors.Wrap(err, "legacy_token_redirect")
	}
	uris, ok := stringList(fields["redirect_uris"])
	if !ok || len(uris) == 0 || len(uris) > maxRedirectURIs {
		return Client{}, errors.Errorf("redirect_uris must list 1 to %d strings", maxRedirectURIs)
	}
	for _, uri := range uris {
		if err := ValidateRedirectURI(uri); err != nil {
			return Client{}, errors.Wrapf(err, "redirect uri %q", uri)
		}
	}
	return Client{ID: id, RedirectURIs: slices.Compact(slices.Sorted(slices.Values(uris))), LegacyTokenRedirect: mode}, nil
}

// ValidateRedirectURI requires a canonical callback that passes the shared redirect rules:
// https (http only on loopback or private development hosts), lower-case host, no userinfo,
// default port, query or fragment, an explicit path without dot segments or escapes, and not
// the Blog host. Registering a private callback is how a production issuer may reach it.
func ValidateRedirectURI(uri string) error {
	checked, reason := inspect(uri, "")
	switch {
	case reason != "":
		return errors.Errorf("is not an allowed redirect target (reject:%s)", reason)
	case checked.blog:
		return errors.New("the Blog hostname is reserved for the built-in blog client")
	case checked.value != uri:
		return errors.New("must be an absolute URL without surrounding whitespace")
	}
	parsed := checked.parsed
	switch {
	case !strings.HasPrefix(uri, parsed.scheme+"://"):
		return errors.New("scheme must be lower case")
	case parsed.query != "" || strings.ContainsAny(uri, "?#"):
		return errors.New("must not contain a query or fragment")
	case strings.HasSuffix(parsed.host, "."):
		return errors.New("host must not end with a dot")
	case parsed.hasPort && parsed.port == defaultPort(parsed.scheme):
		return errors.New("must omit the default port")
	case !strings.HasPrefix(uri, parsed.origin()+"/"):
		return errors.New("host must be lower case and the path must start with /")
	case !canonicalPath(parsed.path):
		return errors.New("path must use unreserved characters without dot segments or escapes")
	}
	return nil
}

// canonicalPath accepts a "/"-led path whose segments browsers serialize unchanged.
func canonicalPath(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	for segment := range strings.SplitSeq(path[1:], "/") {
		if segment == "." || segment == ".." {
			return false
		}
		for i := range len(segment) {
			c := segment[i]
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && !strings.ContainsRune("-._~!$&'()*+,;=:@", rune(c)) {
				return false
			}
		}
	}
	return true
}

// validClientID accepts a lower-case identifier that survives case-insensitive configuration keys.
func validClientID(id string) bool {
	if len(id) < 2 || len(id) > maxClientIDLen || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for i := range len(id) {
		if c := id[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// ValidEntropy accepts the canonical unpadded base64url encoding of exactly 32 bytes.
func ValidEntropy(value string) bool {
	if len(value) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == 32
}

// stringMap normalizes YAML and JSON maps to string keys.
func stringMap(raw any) (map[string]any, bool) {
	switch value := raw.(type) {
	case map[string]any:
		return value, true
	case map[any]any:
		converted := make(map[string]any, len(value))
		for key, item := range value {
			name, ok := key.(string)
			if !ok {
				return nil, false
			}
			converted[name] = item
		}
		return converted, true
	default:
		return nil, false
	}
}

// stringList normalizes YAML and JSON lists of strings.
func stringList(raw any) ([]string, bool) {
	switch value := raw.(type) {
	case []string:
		return slices.Clone(value), true
	case []any:
		list := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			list = append(list, text)
		}
		return list, true
	default:
		return nil, false
	}
}

// Client returns the registration for id. The built-in Blog client is not listed here.
func (p Policy) Client(id string) (Client, bool) {
	for _, client := range p.Clients {
		if client.ID == id {
			return client, true
		}
	}
	return Client{}, false
}

// ValidCodeBinding reports whether clientID may receive a code for exactly redirectURI.
func (p Policy) ValidCodeBinding(clientID, redirectURI string) bool {
	if clientID == BlogClientID {
		return redirectURI == BlogRedirectURI
	}
	client, ok := p.Client(clientID)
	return ok && slices.Contains(client.RedirectURIs, redirectURI)
}

// RedeemOrigin returns the only browser origin that may redeem a code for the binding.
func (p Policy) RedeemOrigin(clientID, redirectURI string) (string, bool) {
	if !p.ValidCodeBinding(clientID, redirectURI) {
		return "", false
	}
	parsed, ok := splitTarget(redirectURI)
	if !ok {
		return "", false
	}
	return parsed.origin(), true
}

// RedeemOriginAllowed reports whether origin belongs to any registered callback, including Blog.
func (p Policy) RedeemOriginAllowed(origin string) bool {
	if origin == BlogRedirectURI {
		return true
	}
	for _, client := range p.Clients {
		for _, uri := range client.RedirectURIs {
			if parsed, ok := splitTarget(uri); ok && parsed.origin() == origin {
				return true
			}
		}
	}
	return false
}

// ownsHostname reports whether one of the client's callbacks is on hostname.
func (c Client) ownsHostname(hostname string) bool {
	for _, uri := range c.RedirectURIs {
		if parsed, ok := splitTarget(uri); ok && parsed.host == hostname {
			return true
		}
	}
	return false
}

// CodeKeyPrefix returns the Redis key namespace for a client's codes. Blog keeps its original namespace.
func CodeKeyPrefix(clientID string) string {
	if clientID == BlogClientID {
		return "sso:blog:code:"
	}
	return fmt.Sprintf("sso:client:%s:code:", clientID)
}
