// Package clientip is the single client-identity rule of the HTTP service.
//
// A request's client address is the connection peer, unless that peer is a
// configured trusted proxy: only then is the forwarding chain consulted, walked
// from the right and stopping at the first address that is not a trusted proxy.
// Every caller that needs the client address (adaptive authentication, the
// Turnstile siteverify remoteip, request and MCP logs) reads it through this
// package so there is exactly one identity policy.
package clientip

import (
	"net/netip"
	"net/textproto"
	"slices"
	"strings"
	"unicode"

	"github.com/Laisky/errors/v2"
	"github.com/Laisky/zap"
	"github.com/gin-gonic/gin"
)

const (
	// ConfigKeyTrustedProxies lists the proxy addresses and networks whose
	// forwarding headers are believed. Unset selects DefaultTrustedProxies; an
	// explicit empty list trusts no proxy.
	ConfigKeyTrustedProxies = "settings.web.trusted_proxies"
	// ConfigKeyHeaders lists the forwarding headers read from a trusted proxy,
	// in order. Unset selects DefaultHeaders; an explicit empty list disables
	// forwarding headers entirely.
	ConfigKeyHeaders = "settings.web.client_ip_headers"

	// HeaderXForwardedFor is the forwarding header read by default.
	HeaderXForwardedFor = "X-Forwarded-For"

	sourceDefault = "default"
	sourceConfig  = "config"
)

// defaultTrustedProxies are the private networks a co-located reverse proxy
// connects from: loopback, RFC 1918, RFC 6598 carrier-grade NAT (Tailscale
// assigns addresses here), IPv6 loopback and RFC 4193 unique local addresses.
// Public addresses are never trusted unless configured.
var defaultTrustedProxies = []string{
	"127.0.0.0/8",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10",
	"::1/128",
	"fc00::/7",
}

// DefaultTrustedProxies returns the networks trusted when
// ConfigKeyTrustedProxies is unset.
func DefaultTrustedProxies() []string {
	return slices.Clone(defaultTrustedProxies)
}

// DefaultHeaders returns the forwarding headers read when ConfigKeyHeaders is
// unset. Only X-Forwarded-For is read: a second header is never consulted when
// the first is malformed, because falling back across headers lets a client
// that can inject the second one choose its own address.
func DefaultHeaders() []string {
	return []string{HeaderXForwardedFor}
}

// ConfigGetter returns the raw configuration value stored under a dotted key,
// or nil when the key is unset.
type ConfigGetter func(key string) any

// Policy is a validated client-identity policy.
type Policy struct {
	trustedProxies []netip.Prefix
	headers        []string
	proxiesSource  string
	headersSource  string
}

// LoadPolicy builds the policy from configuration. It returns an error naming
// the offending entry when a configured value is not a valid IP address, CIDR
// or header name, so a typo fails startup instead of silently trusting more.
func LoadPolicy(get ConfigGetter) (Policy, error) {
	if get == nil {
		return Policy{}, errors.New("client identity config getter is nil")
	}

	policy := Policy{proxiesSource: sourceDefault, headersSource: sourceDefault}
	proxies := DefaultTrustedProxies()
	if raw := get(ConfigKeyTrustedProxies); raw != nil {
		entries, err := parseStringList(ConfigKeyTrustedProxies, raw)
		if err != nil {
			return Policy{}, err
		}
		proxies = entries
		policy.proxiesSource = sourceConfig
	}

	headers := DefaultHeaders()
	if raw := get(ConfigKeyHeaders); raw != nil {
		entries, err := parseStringList(ConfigKeyHeaders, raw)
		if err != nil {
			return Policy{}, err
		}
		headers = entries
		policy.headersSource = sourceConfig
	}

	var err error
	if policy.trustedProxies, err = parseTrustedProxies(proxies); err != nil {
		return Policy{}, err
	}
	if policy.headers, err = parseHeaders(headers); err != nil {
		return Policy{}, err
	}

	return policy, nil
}

// NewPolicy builds a policy from explicit trusted proxies and headers. An empty
// trustedProxies trusts no proxy; empty headers disable forwarding headers.
func NewPolicy(trustedProxies []string, headers []string) (Policy, error) {
	prefixes, err := parseTrustedProxies(trustedProxies)
	if err != nil {
		return Policy{}, err
	}
	names, err := parseHeaders(headers)
	if err != nil {
		return Policy{}, err
	}

	return Policy{trustedProxies: prefixes, headers: names, proxiesSource: sourceConfig, headersSource: sourceConfig}, nil
}

// TrustedProxies returns the trusted networks in canonical CIDR form.
func (p Policy) TrustedProxies() []string {
	result := make([]string, 0, len(p.trustedProxies))
	for _, prefix := range p.trustedProxies {
		result = append(result, prefix.String())
	}
	return result
}

// Headers returns the forwarding headers read from a trusted proxy, in order.
func (p Policy) Headers() []string {
	return slices.Clone(p.headers)
}

// TrustsEveryPeer reports whether a configured network covers the whole IPv4
// or IPv6 address space, which lets any client choose its own address.
func (p Policy) TrustsEveryPeer() bool {
	for _, prefix := range p.trustedProxies {
		if prefix.Bits() == 0 {
			return true
		}
	}
	return false
}

// Apply configures engine so its ClientIP follows the policy: forwarding
// headers are honored only from a trusted peer, platform headers are never
// trusted, and an empty proxy list makes ClientIP the connection peer.
func (p Policy) Apply(engine *gin.Engine) error {
	if engine == nil {
		return errors.New("gin engine is nil")
	}

	engine.TrustedPlatform = ""
	engine.RemoteIPHeaders = p.Headers()
	engine.ForwardedByClientIP = len(p.headers) > 0

	var proxies []string
	if len(p.trustedProxies) > 0 {
		proxies = p.TrustedProxies()
	}
	if err := engine.SetTrustedProxies(proxies); err != nil {
		return errors.Wrap(err, "set gin trusted proxies")
	}

	return nil
}

// LogFields describes the effective policy for the startup log.
func (p Policy) LogFields() []zap.Field {
	return []zap.Field{
		zap.Strings("trusted_proxies", p.TrustedProxies()),
		zap.String("trusted_proxies_source", p.proxiesSource),
		zap.Strings("client_ip_headers", p.Headers()),
		zap.String("client_ip_headers_source", p.headersSource),
		zap.Bool("trusts_every_peer", p.TrustsEveryPeer()),
	}
}

// parseStringList accepts a YAML sequence of strings or a comma or space
// separated string, which is how an environment variable override arrives.
func parseStringList(key string, raw any) ([]string, error) {
	switch value := raw.(type) {
	case []string:
		return slices.Clone(value), nil
	case []any:
		result := make([]string, 0, len(value))
		for i, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, errors.Errorf("%s[%d] must be a string, got %T", key, i, item)
			}
			result = append(result, text)
		}
		return result, nil
	case string:
		return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }), nil
	default:
		return nil, errors.Errorf("%s must be a list of strings, got %T", key, raw)
	}
}

// ipv4MappedPrefixBits is the length of the ::ffff:0:0/96 IPv4-mapped block.
const ipv4MappedPrefixBits = 96

// parseTrustedProxies validates IP addresses and CIDRs and returns them as
// canonical prefixes, in the form gin matches peers against (see
// normalizeTrustedPrefix). A CIDR with host bits set is rejected because it
// usually means the operator wrote a host where a network was expected, or
// vice versa.
func parseTrustedProxies(entries []string) ([]netip.Prefix, error) {
	result := make([]netip.Prefix, 0, len(entries))
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			return nil, errors.Errorf("%s contains an empty entry", ConfigKeyTrustedProxies)
		}

		var prefix netip.Prefix
		if strings.Contains(entry, "/") {
			parsed, err := netip.ParsePrefix(entry)
			if err != nil {
				return nil, errors.Wrapf(err, "%s entry %q is not a valid CIDR", ConfigKeyTrustedProxies, entry)
			}
			prefix = parsed
		} else {
			addr, err := netip.ParseAddr(entry)
			if err != nil {
				return nil, errors.Wrapf(err, "%s entry %q is not a valid IP address", ConfigKeyTrustedProxies, entry)
			}
			if addr.Zone() != "" {
				return nil, errors.Errorf("%s entry %q must not carry an IPv6 zone", ConfigKeyTrustedProxies, entry)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}

		normalized, err := normalizeTrustedPrefix(entry, prefix)
		if err != nil {
			return nil, err
		}
		if normalized.Masked() != normalized {
			return nil, errors.Errorf("%s entry %q has host bits set; use %q for the network or a bare address for one host",
				ConfigKeyTrustedProxies, entry, normalized.Masked().String())
		}
		result = appendUniquePrefix(result, normalized)
	}

	return result, nil
}

// normalizeTrustedPrefix rewrites an IPv4-mapped IPv6 prefix (::ffff:a.b.c.d/n)
// as the IPv4 prefix it denotes. gin matches peers with net.IPNet.Contains,
// which treats a mapped network of /96 or longer as the IPv4 network
// a.b.c.d/(n-96) and matches IPv4 peers against it, so ::ffff:0:0/96 trusts
// every IPv4 peer. Rewriting keeps TrustsEveryPeer, the startup log and the
// networks handed to gin in agreement. A mapped prefix shorter than /96 is
// rejected: it does not cover a whole IPv4 network and gin would match it as
// an IPv6 network that no IPv4 peer reaches.
func normalizeTrustedPrefix(entry string, prefix netip.Prefix) (netip.Prefix, error) {
	if !prefix.Addr().Is4In6() {
		return prefix, nil
	}
	if prefix.Bits() < ipv4MappedPrefixBits {
		return netip.Prefix{}, errors.Errorf("%s entry %q is an IPv4-mapped prefix shorter than /%d; write the IPv4 network instead",
			ConfigKeyTrustedProxies, entry, ipv4MappedPrefixBits)
	}
	return netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-ipv4MappedPrefixBits), nil
}

// appendUniquePrefix appends prefix unless it is already present.
func appendUniquePrefix(prefixes []netip.Prefix, prefix netip.Prefix) []netip.Prefix {
	if slices.Contains(prefixes, prefix) {
		return prefixes
	}
	return append(prefixes, prefix)
}

// parseHeaders validates header names and returns them canonicalized.
func parseHeaders(entries []string) ([]string, error) {
	result := make([]string, 0, len(entries))
	for _, raw := range entries {
		name := strings.TrimSpace(raw)
		if !isHeaderToken(name) {
			return nil, errors.Errorf("%s entry %q is not a valid header name", ConfigKeyHeaders, raw)
		}
		name = textproto.CanonicalMIMEHeaderKey(name)
		if !slices.Contains(result, name) {
			result = append(result, name)
		}
	}

	return result, nil
}

// isHeaderToken reports whether name is a non-empty RFC 9110 field-name made
// of letters, digits and hyphens, the only form forwarding headers take.
func isHeaderToken(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r > unicode.MaxASCII || (!unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-') {
			return false
		}
	}
	return true
}
