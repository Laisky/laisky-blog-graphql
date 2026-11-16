package auth

import (
	"net/url"
	"strings"

	errors "github.com/Laisky/errors/v2"
)

const (
	// PublicOriginConfigKey configures the public origin of this MCP
	// deployment, the RFC 9728 resource identifier whose root serves the
	// protected-resource metadata document.
	PublicOriginConfigKey = "settings.mcp.public_origin"
	// DefaultPublicOrigin is the origin of the primary deployment, used when
	// PublicOriginConfigKey is unset.
	DefaultPublicOrigin = "https://mcp.laisky.com"
	// ResourceMetadataPath is the RFC 9728 well-known path of the
	// protected-resource metadata document.
	ResourceMetadataPath = "/.well-known/oauth-protected-resource"
	// DefaultResourceMetadataURL is the metadata document of DefaultPublicOrigin.
	DefaultResourceMetadataURL = DefaultPublicOrigin + ResourceMetadataPath
)

// ParsePublicOrigin validates an operator-configured public origin and
// returns it in canonical "scheme://host[:port]" form. It must be an absolute
// http(s) URL without user info, path, query or fragment: the metadata URL
// derives from configuration only, never from a request's Host header.
func ParsePublicOrigin(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", errors.Wrapf(err, "%s must be an absolute http(s) origin", PublicOriginConfigKey)
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch {
	case !isHTTPScheme(scheme), parsed.Host == "", parsed.Hostname() == "":
		return "", errors.Errorf("%s must be an absolute http(s) origin, got %q", PublicOriginConfigKey, trimmed)
	case parsed.User != nil:
		return "", errors.Errorf("%s must not carry user info", PublicOriginConfigKey)
	case parsed.Path != "" && parsed.Path != "/", parsed.RawQuery != "", parsed.ForceQuery, parsed.Fragment != "", parsed.RawFragment != "":
		return "", errors.Errorf("%s must be an origin without path, query or fragment, got %q", PublicOriginConfigKey, parsed.Redacted())
	}
	return scheme + "://" + strings.ToLower(parsed.Host), nil
}

// LoadPublicOrigin reads PublicOriginConfigKey through get; an absent or
// empty value keeps DefaultPublicOrigin.
func LoadPublicOrigin(get func(string) any) (string, error) {
	if get == nil {
		return DefaultPublicOrigin, nil
	}
	switch value := get(PublicOriginConfigKey).(type) {
	case nil:
		return DefaultPublicOrigin, nil
	case string:
		if strings.TrimSpace(value) == "" {
			return DefaultPublicOrigin, nil
		}
		return ParsePublicOrigin(value)
	default:
		return "", errors.Errorf("%s must be a string", PublicOriginConfigKey)
	}
}

// ResourceMetadataURL returns the RFC 9728 §3.1 metadata URL of the resource
// identified by origin, as returned by ParsePublicOrigin.
func ResourceMetadataURL(origin string) string {
	return strings.TrimSuffix(origin, "/") + ResourceMetadataPath
}
