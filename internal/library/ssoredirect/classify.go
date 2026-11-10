// Package ssoredirect is the single SSO redirect policy shared by the issuer backend and
// frontend. web/src/lib/sso-redirect-policy.ts implements the same algorithm, and both
// are pinned by the shared vectors in web/src/lib/sso-redirect-policy.v1.json so the
// two enforcement points cannot drift. See docs/manual/sso.md for the contract.
package ssoredirect

import (
	"slices"
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
	ReasonDevelopmentTarget = "development_target_requires_registration"
)

// hostClass separates production hosts from loopback and private development hosts.
type hostClass int

const (
	// hostProduction is laisky.com or a subdomain; only https is accepted.
	hostProduction hostClass = iota
	// hostLoopback is localhost, 127.0.0.0/8 or ::1; the request never leaves the device.
	hostLoopback
	// hostPrivate is an RFC 1918, RFC 6598 (CGNAT) or fc00::/7 literal shared by a network.
	hostPrivate
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

// inspection is a target that passed the syntax, host and transport rules.
type inspection struct {
	value    string
	parsed   target
	class    hostClass
	blog     bool
	issuer   target
	issuerOK bool
}

// inspect applies the rules shared by every target and every registration: printable
// ASCII, no backslash before the query or fragment, bounded length, an absolute http(s)
// URL or an issuer-relative path, no userinfo, a canonical host and port, and https on
// production hosts. Blog targets stop after the host is known; the Blog protocol owns them.
func inspect(raw, issuerOrigin string) (inspection, string) {
	value := trimASCIISpace(raw)
	if value == "" {
		return inspection{}, ReasonMissing
	}
	// WHATWG URL parsing reads a backslash as "/" in the scheme, authority and path, where
	// other parsers keep it. Browsers leave it unencoded in the query and fragment, where it is data.
	boundary := strings.IndexAny(value, "?#")
	if boundary < 0 {
		boundary = len(value)
	}
	for i := range len(value) {
		if c := value[i]; c < 0x21 || c > 0x7e || c == '\\' && i < boundary {
			return inspection{}, ReasonInvalidCharacter
		}
	}
	if len(value) > MaxTargetLength {
		return inspection{}, ReasonTooLong
	}

	var result inspection
	result.issuer, result.issuerOK = splitTarget(trimASCIISpace(issuerOrigin))
	result.issuerOK = result.issuerOK && !result.issuer.userinfo && result.issuer.query == "" && !result.issuer.fragment &&
		(result.issuer.path == "" || result.issuer.path == "/")
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		if !result.issuerOK {
			return inspection{}, ReasonUnsupportedTarget
		}
		value = result.issuer.origin() + value
	}
	parsed, ok := splitTarget(value)
	if !ok {
		return inspection{}, ReasonUnsupportedTarget
	}
	result.value, result.parsed = value, parsed
	if strings.TrimSuffix(parsed.host, ".") == BlogHostname {
		result.blog = true
		return result, ""
	}
	if parsed.userinfo {
		return inspection{}, ReasonUserinfo
	}
	class, reason := classifyHost(parsed.host)
	if reason != "" {
		return inspection{}, reason
	}
	if parsed.hasPort && !validPort(parsed.port) {
		return inspection{}, ReasonInvalidPort
	}
	if class == hostProduction && parsed.scheme != schemeHTTPS {
		return inspection{}, ReasonInsecureScheme
	}
	result.class = class
	return result, ""
}

// developmentIssuer reports whether the issuer page itself runs on a loopback or private host.
func (i inspection) developmentIssuer() bool {
	if !i.issuerOK {
		return false
	}
	class, reason := classifyHost(i.issuer.host)
	return reason == "" && class != hostProduction
}

// Classify decides how the issuer may hand a session to raw. issuerOrigin is the
// issuer page origin (for example https://sso.laisky.com); it resolves root-relative
// paths and identifies issuer pages. The checks run on the raw string, so URL parser
// repairs (backslashes, tabs, numeric IPv4 spellings, userinfo) cannot change the outcome.
//
// Loopback targets keep the legacy handoff from any issuer, because the request never
// leaves the device. A private, CGNAT or ULA target is shared by a network, so a
// production issuer hands it the bearer only at an exact registered callback whose
// client allows the legacy handoff; a development issuer keeps the legacy handoff.
func Classify(raw, issuerOrigin string, policy Policy) Decision {
	checked, reason := inspect(raw, issuerOrigin)
	if reason != "" {
		return reject(reason)
	}
	value, parsed := checked.value, checked.parsed
	if checked.blog {
		return Decision{Kind: KindBlog, Target: value}
	}

	names := queryNames(parsed.query)
	if names["sso_client_id"] {
		return classifyCodeRequest(parsed, policy)
	}
	if names["sso_flow"] {
		return reject(ReasonInvalidCode)
	}
	if checked.issuerOK && parsed.origin() == checked.issuer.origin() {
		return Decision{Kind: KindSelf, Target: value}
	}
	gated := checked.class == hostPrivate && !checked.developmentIssuer()
	hostname := strings.TrimSuffix(parsed.host, ".")
	for _, client := range policy.Clients {
		if !client.ownsHostname(hostname) {
			continue
		}
		if client.LegacyTokenRedirect != ModeAllow || policy.LegacyTokenRedirect != ModeAllow {
			return reject(ReasonCodeFlowRequired)
		}
		if gated && !slices.Contains(client.RedirectURIs, parsed.callback()) {
			return reject(ReasonDevelopmentTarget)
		}
		return Decision{Kind: KindLegacy, Target: value}
	}
	if gated {
		return reject(ReasonDevelopmentTarget)
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
	redirectURI := parsed.callback()
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

// callback returns the raw target before its query or fragment, for exact registry comparison.
func (t target) callback() string {
	if end := strings.IndexAny(t.raw, "?#"); end >= 0 {
		return t.raw[:end]
	}
	return t.raw
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

// classifyHost reports whether host is a production host (https only), a loopback host or a
// private development host, or returns the rejection reason.
func classifyHost(host string) (hostClass, string) {
	if strings.HasPrefix(host, "[") {
		if !strings.HasSuffix(host, "]") {
			return hostProduction, ReasonInvalidHost
		}
		address, ok := parseIPv6(host[1 : len(host)-1])
		switch {
		case !ok:
			return hostProduction, ReasonInvalidHost
		case isLoopbackIPv6(address):
			return hostLoopback, ""
		case address[0]&0xfe00 == 0xfc00:
			return hostPrivate, ""
		default:
			return hostProduction, ReasonHostNotAllowed
		}
	}
	name := strings.TrimSuffix(host, ".")
	if name == "" {
		return hostProduction, ReasonInvalidHost
	}
	last := name[strings.LastIndexByte(name, '.')+1:]
	if isDigits(last) || hasPrefixFold(last, "0x") {
		address, ok := parseIPv4(host)
		switch {
		case !ok:
			return hostProduction, ReasonInvalidHost
		case address[0] == 127:
			return hostLoopback, ""
		case isPrivateIPv4(address):
			return hostPrivate, ""
		default:
			return hostProduction, ReasonHostNotAllowed
		}
	}
	switch {
	case !isDNSName(name):
		return hostProduction, ReasonInvalidHost
	case name == "laisky.com" || strings.HasSuffix(name, ".laisky.com"):
		return hostProduction, ""
	case name == "localhost":
		return hostLoopback, ""
	default:
		return hostProduction, ReasonHostNotAllowed
	}
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

// isPrivateIPv4 covers RFC 1918 private and RFC 6598 shared (CGNAT, e.g. Tailscale) ranges.
func isPrivateIPv4(a [4]int) bool {
	return a[0] == 10 || a[0] == 172 && a[1] >= 16 && a[1] <= 31 ||
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
