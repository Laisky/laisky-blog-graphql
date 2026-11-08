// Package ssoredirect is the single SSO redirect policy shared by the issuer backend and
// frontend. web/src/lib/sso-redirect-policy.ts implements the same algorithm, and both
// are pinned by the shared vectors in web/src/lib/sso-redirect-policy.v1.json so the
// two enforcement points cannot drift. See docs/manual/sso.md for the contract.
package ssoredirect

import (
	"strconv"
	"strings"
)

const (
	// MaxTargetLength bounds a redirect target before any parsing or allocation.
	MaxTargetLength = 2048
	// BlogHostname is the built-in Blog client, whose wire protocol is defined separately
	// by web/src/lib/blog-sso-protocol.v1.json and is never classified by this policy.
	BlogHostname = "blog.laisky.com"
	// BlogClientID is the built-in Blog client identifier.
	BlogClientID = "blog"
	// BlogRedirectURI is the exact registered Blog callback.
	BlogRedirectURI = "https://blog.laisky.com"

	schemeHTTPS = "https"
	schemeHTTP  = "http"
)

// Kind is the single issuance path a redirect target may take.
type Kind string

const (
	// KindReject refuses the target; nothing is issued and no navigation happens.
	KindReject Kind = "reject"
	// KindBlog delegates the target to the built-in Blog code/bootstrap protocol.
	KindBlog Kind = "blog"
	// KindClientCode is a registered client's code request; only a single-use code is issued.
	KindClientCode Kind = "client"
	// KindSelf is a page on the issuer's own origin; the session is already stored there.
	KindSelf Kind = "self"
	// KindLegacy is the legacy bearer handoff (sso_token in the query) for an unregistered target.
	KindLegacy Kind = "legacy"
)

// Rejection reasons. They are stable identifiers shared with the frontend and tests.
const (
	ReasonMissing           = "missing"
	ReasonInvalidCharacter  = "invalid_character"
	ReasonTooLong           = "too_long"
	ReasonUnsupportedTarget = "unsupported_reference"
	ReasonUserinfo          = "userinfo"
	ReasonInvalidHost       = "invalid_host"
	ReasonInvalidPort       = "invalid_port"
	ReasonHostNotAllowed    = "host_not_allowed"
	ReasonInsecureScheme    = "insecure_scheme"
	ReasonInvalidCode       = "invalid_code_request"
	ReasonUnregisteredCode  = "unregistered_client"
	ReasonCodeFlowRequired  = "code_flow_required"
	ReasonLegacyDisabled    = "legacy_redirect_disabled"
)

// Decision is the classification of one redirect target.
type Decision struct {
	// Kind is the issuance path.
	Kind Kind
	// Reason names the failed rule when Kind is KindReject.
	Reason string
	// ClientID and RedirectURI identify the registration when Kind is KindClientCode.
	ClientID    string
	RedirectURI string
	// Target is the absolute target, with an issuer-relative path resolved.
	Target string
}

// String renders the decision in the shared vector notation, for example "client:accounting".
func (d Decision) String() string {
	switch d.Kind {
	case KindReject:
		return "reject:" + d.Reason
	case KindClientCode:
		return "client:" + d.ClientID
	default:
		return string(d.Kind)
	}
}

// Allowed reports whether the target may be used at all.
func (d Decision) Allowed() bool {
	return d.Kind != KindReject && d.Kind != ""
}

// codeMarkers lists the exact query of a registered client's code request.
var codeMarkers = []string{"sso_flow", "sso_client_id", "sso_state", "sso_challenge", "sso_challenge_method"}

// target holds the pieces of an absolute http(s) target split without normalization.
type target struct {
	raw      string
	scheme   string
	userinfo bool
	host     string
	hasPort  bool
	port     string
	path     string
	query    string
	fragment bool
}

// Classify decides how the issuer may hand a session to raw. issuerOrigin is the
// issuer page origin (for example https://sso.laisky.com); it resolves root-relative
// paths and identifies issuer pages. The checks run on the raw string, so URL parser
// repairs (backslashes, tabs, numeric IPv4 spellings, userinfo) cannot change the outcome.
func Classify(raw, issuerOrigin string, policy Policy) Decision {
	value := trimASCIISpace(raw)
	if value == "" {
		return reject(ReasonMissing)
	}
	for i := range len(value) {
		if c := value[i]; c < 0x21 || c > 0x7e || c == '\\' {
			return reject(ReasonInvalidCharacter)
		}
	}
	if len(value) > MaxTargetLength {
		return reject(ReasonTooLong)
	}

	issuer, issuerOK := splitTarget(trimASCIISpace(issuerOrigin))
	issuerOK = issuerOK && !issuer.userinfo && issuer.query == "" && !issuer.fragment && (issuer.path == "" || issuer.path == "/")
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		if !issuerOK {
			return reject(ReasonUnsupportedTarget)
		}
		value = issuer.origin() + value
	}
	parsed, ok := splitTarget(value)
	if !ok {
		return reject(ReasonUnsupportedTarget)
	}
	if strings.TrimSuffix(parsed.host, ".") == BlogHostname {
		return Decision{Kind: KindBlog, Target: value}
	}
	if parsed.userinfo {
		return reject(ReasonUserinfo)
	}
	production, reason := classifyHost(parsed.host)
	if reason != "" {
		return reject(reason)
	}
	if parsed.hasPort && !validPort(parsed.port) {
		return reject(ReasonInvalidPort)
	}
	if production && parsed.scheme != schemeHTTPS {
		return reject(ReasonInsecureScheme)
	}

	names := queryNames(parsed.query)
	if names["sso_client_id"] {
		return classifyCodeRequest(parsed, policy)
	}
	if names["sso_flow"] {
		return reject(ReasonInvalidCode)
	}
	if issuerOK && parsed.origin() == issuer.origin() {
		return Decision{Kind: KindSelf, Target: value}
	}
	hostname := strings.TrimSuffix(parsed.host, ".")
	for _, client := range policy.Clients {
		if !client.ownsHostname(hostname) {
			continue
		}
		if client.LegacyTokenRedirect == ModeAllow && policy.LegacyTokenRedirect == ModeAllow {
			return Decision{Kind: KindLegacy, Target: value}
		}
		return reject(ReasonCodeFlowRequired)
	}
	if policy.LegacyTokenRedirect != ModeAllow {
		return reject(ReasonLegacyDisabled)
	}
	return Decision{Kind: KindLegacy, Target: value}
}

// classifyCodeRequest admits only the exact registered callback followed by the five code markers.
func classifyCodeRequest(parsed target, policy Policy) Decision {
	if parsed.fragment {
		return reject(ReasonInvalidCode)
	}
	values := make(map[string]string, len(codeMarkers))
	for piece := range strings.SplitSeq(parsed.query, "&") {
		name, value, found := strings.Cut(piece, "=")
		if !found || !isCodeMarker(name) {
			return reject(ReasonInvalidCode)
		}
		if _, duplicate := values[name]; duplicate {
			return reject(ReasonInvalidCode)
		}
		values[name] = value
	}
	if len(values) != len(codeMarkers) || values["sso_flow"] != "code" || values["sso_challenge_method"] != "S256" ||
		!ValidEntropy(values["sso_state"]) || !ValidEntropy(values["sso_challenge"]) || !validClientID(values["sso_client_id"]) {
		return reject(ReasonInvalidCode)
	}
	client, ok := policy.Client(values["sso_client_id"])
	if !ok {
		return reject(ReasonUnregisteredCode)
	}
	redirectURI, _, _ := strings.Cut(parsed.raw, "?")
	for _, registered := range client.RedirectURIs {
		if registered == redirectURI {
			return Decision{Kind: KindClientCode, ClientID: client.ID, RedirectURI: registered, Target: parsed.raw}
		}
	}
	return reject(ReasonInvalidCode)
}

// isCodeMarker reports whether name is one of the exact, undecoded code markers.
func isCodeMarker(name string) bool {
	for _, marker := range codeMarkers {
		if name == marker {
			return true
		}
	}
	return false
}

// reject builds a refusal for reason.
func reject(reason string) Decision {
	return Decision{Kind: KindReject, Reason: reason}
}

// trimASCIISpace removes only ASCII whitespace, identically to the frontend.
func trimASCIISpace(value string) string {
	return strings.Trim(value, " \t\n\v\f\r")
}

// splitTarget splits an absolute http(s) target into its raw components.
func splitTarget(value string) (target, bool) {
	parsed := target{raw: value}
	var rest string
	switch {
	case hasPrefixFold(value, schemeHTTPS+"://"):
		parsed.scheme, rest = schemeHTTPS, value[len(schemeHTTPS+"://"):]
	case hasPrefixFold(value, schemeHTTP+"://"):
		parsed.scheme, rest = schemeHTTP, value[len(schemeHTTP+"://"):]
	default:
		return target{}, false
	}
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	authority, tail := rest[:end], rest[end:]
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		parsed.userinfo, authority = true, authority[at+1:]
	}
	if strings.HasPrefix(authority, "[") {
		closing := strings.IndexByte(authority, ']')
		if closing < 0 {
			parsed.host = strings.ToLower(authority)
			return parsed, true
		}
		parsed.host, authority = strings.ToLower(authority[:closing+1]), authority[closing+1:]
		if authority != "" {
			if authority[0] != ':' {
				parsed.host += authority
				return parsed, true
			}
			parsed.hasPort, parsed.port = true, authority[1:]
		}
	} else if host, port, found := strings.Cut(authority, ":"); found {
		parsed.host, parsed.hasPort, parsed.port = strings.ToLower(host), true, port
	} else {
		parsed.host = strings.ToLower(authority)
	}
	if hash := strings.IndexByte(tail, '#'); hash >= 0 {
		parsed.fragment, tail = true, tail[:hash]
	}
	parsed.path = tail
	if question := strings.IndexByte(tail, '?'); question >= 0 {
		parsed.path, parsed.query = tail[:question], tail[question+1:]
	}
	return parsed, true
}

// origin renders the comparable origin: lower-case scheme and host, without a default port.
func (t target) origin() string {
	origin := t.scheme + "://" + t.host
	if t.hasPort && t.port != defaultPort(t.scheme) {
		origin += ":" + t.port
	}
	return origin
}

// defaultPort returns the implicit port of scheme.
func defaultPort(scheme string) string {
	if scheme == schemeHTTPS {
		return "443"
	}
	return "80"
}

// hasPrefixFold reports whether value starts with prefix, ignoring ASCII case.
func hasPrefixFold(value, prefix string) bool {
	return len(value) >= len(prefix) && strings.EqualFold(value[:len(prefix)], prefix)
}

// validPort accepts 1 to 65535 without leading zeros or signs.
func validPort(port string) bool {
	if port == "" || len(port) > 5 || port[0] == '0' {
		return false
	}
	for i := range len(port) {
		if port[i] < '0' || port[i] > '9' {
			return false
		}
	}
	value, err := strconv.Atoi(port)
	return err == nil && value >= 1 && value <= 65535
}

// classifyHost reports whether host is a production host (https only) or an explicit
// development host (http allowed), or returns the rejection reason.
func classifyHost(host string) (production bool, reason string) {
	if strings.HasPrefix(host, "[") {
		if !strings.HasSuffix(host, "]") {
			return false, ReasonInvalidHost
		}
		address, ok := parseIPv6(host[1 : len(host)-1])
		if !ok {
			return false, ReasonInvalidHost
		}
		if isLoopbackIPv6(address) || address[0]&0xfe00 == 0xfc00 {
			return false, ""
		}
		return false, ReasonHostNotAllowed
	}
	name := strings.TrimSuffix(host, ".")
	if name == "" {
		return false, ReasonInvalidHost
	}
	last := name[strings.LastIndexByte(name, '.')+1:]
	if isDigits(last) || hasPrefixFold(last, "0x") {
		address, ok := parseIPv4(host)
		if !ok {
			return false, ReasonInvalidHost
		}
		if isDevelopmentIPv4(address) {
			return false, ""
		}
		return false, ReasonHostNotAllowed
	}
	if !isDNSName(name) {
		return false, ReasonInvalidHost
	}
	if name == "laisky.com" || strings.HasSuffix(name, ".laisky.com") {
		return true, ""
	}
	if name == "localhost" {
		return false, ""
	}
	return false, ReasonHostNotAllowed
}

// isDNSName accepts dot-separated labels of lower-case letters, digits and hyphens.
func isDNSName(name string) bool {
	for label := range strings.SplitSeq(name, ".") {
		if label == "" {
			return false
		}
		for i := range len(label) {
			if c := label[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

// isDigits reports whether value is a non-empty run of ASCII digits.
func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

// parseIPv4 accepts only canonical dotted-decimal IPv4 without leading zeros.
func parseIPv4(host string) ([4]int, bool) {
	var address [4]int
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return address, false
	}
	for i, part := range parts {
		if !isDigits(part) || len(part) > 3 || len(part) > 1 && part[0] == '0' {
			return address, false
		}
		value, err := strconv.Atoi(part)
		if err != nil || value > 255 {
			return address, false
		}
		address[i] = value
	}
	return address, true
}

// isDevelopmentIPv4 covers loopback, RFC 1918 private and RFC 6598 shared (CGNAT, e.g. Tailscale) ranges.
func isDevelopmentIPv4(a [4]int) bool {
	return a[0] == 127 || a[0] == 10 || a[0] == 172 && a[1] >= 16 && a[1] <= 31 ||
		a[0] == 192 && a[1] == 168 || a[0] == 100 && a[1] >= 64 && a[1] <= 127
}

// parseIPv6 parses lower-case hexadecimal IPv6 into its eight groups, without embedded IPv4 or zone identifiers.
func parseIPv6(text string) ([8]uint16, bool) {
	var address [8]uint16
	if text == "" {
		return address, false
	}
	for i := range len(text) {
		if c := text[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') && c != ':' {
			return address, false
		}
	}
	head, tail, compressed := strings.Cut(text, "::")
	if compressed && strings.Contains(tail, "::") {
		return address, false
	}
	headGroups, ok := ipv6Groups(head)
	if !ok {
		return address, false
	}
	var tailGroups []uint16
	if compressed {
		if tailGroups, ok = ipv6Groups(tail); !ok || len(headGroups)+len(tailGroups) > 7 {
			return address, false
		}
	} else if len(headGroups) != 8 {
		return address, false
	}
	copy(address[:], headGroups)
	copy(address[8-len(tailGroups):], tailGroups)
	return address, true
}

// ipv6Groups parses colon-separated groups of one to four hexadecimal digits.
func ipv6Groups(text string) ([]uint16, bool) {
	if text == "" {
		return nil, true
	}
	parts := strings.Split(text, ":")
	groups := make([]uint16, 0, len(parts))
	for _, part := range parts {
		if part == "" || len(part) > 4 {
			return nil, false
		}
		value, err := strconv.ParseUint(part, 16, 16)
		if err != nil {
			return nil, false
		}
		groups = append(groups, uint16(value))
	}
	return groups, true
}

// isLoopbackIPv6 reports whether address is ::1.
func isLoopbackIPv6(address [8]uint16) bool {
	return address == [8]uint16{7: 1}
}

// queryNames returns the decoded parameter names of a raw query, splitting only on "&".
// "+" decodes to a space and valid %XX escapes decode; invalid escapes stay literal.
func queryNames(query string) map[string]bool {
	names := make(map[string]bool)
	if query == "" {
		return names
	}
	for piece := range strings.SplitSeq(query, "&") {
		name, _, _ := strings.Cut(piece, "=")
		names[decodeComponent(name)] = true
	}
	return names
}

// decodeComponent applies application/x-www-form-urlencoded name decoding.
func decodeComponent(value string) string {
	var decoded strings.Builder
	for i := 0; i < len(value); i++ {
		switch c := value[i]; {
		case c == '+':
			decoded.WriteByte(' ')
		case c == '%' && i+2 < len(value) && isHex(value[i+1]) && isHex(value[i+2]):
			decoded.WriteByte(unhex(value[i+1])<<4 | unhex(value[i+2]))
			i += 2
		default:
			decoded.WriteByte(c)
		}
	}
	return decoded.String()
}

// isHex reports whether c is an ASCII hexadecimal digit.
func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// unhex converts one hexadecimal digit.
func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}
