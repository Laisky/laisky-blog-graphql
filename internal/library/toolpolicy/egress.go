package toolpolicy

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
)

// ErrEgressUnverified reports that a render result carried no evidence of which
// origins it actually contacted.
//
// This is deliberately distinct from a policy violation. An absent chain is not
// proof of safety and not proof of a breach; it means the renderer did not
// report, so nothing was checked. A caller decides whether to accept that.
var ErrEgressUnverified = errors.New("renderer reported no request chain; egress is unverified")

// Admission is what request admission actually decided, including the exact
// addresses it validated.
//
// Publishing the addresses is the point. A verdict of "this URL is allowed" is
// not enough for a separate renderer host: by the time the renderer resolves
// the name again the answer can differ, which is the DNS-rebinding window that
// an admission-time lookup alone cannot close. The renderer must connect to
// these addresses and no others.
type Admission struct {
	// URL is the normalized target, including its path and query. It is the
	// fetch target, not a log field; use URLForLog before logging it.
	URL string
	// Host is the lowercased hostname with any trailing dot removed.
	Host string
	// Port is the effective TCP port, defaulted from the scheme.
	Port int
	// Addresses are the admitted public addresses, in canonical text form.
	Addresses []string
}

// EgressPolicy is the connection contract handed to the renderer alongside a
// crawl task. A renderer that honors only these fields still cannot reach a
// non-public address, follow an unbounded redirect chain, or silently pull
// subresources from another origin.
type EgressPolicy struct {
	// Host is the only hostname this task may resolve.
	Host string `json:"host"`
	// Addresses are the only addresses the renderer may connect to for Host.
	Addresses []string `json:"addresses"`
	// MaxRedirects bounds the hops after the initial request. Zero means the
	// renderer must not follow any redirect.
	MaxRedirects int `json:"max_redirects"`
	// AllowSubresources permits loading page subresources. When false the
	// renderer must fetch only the document itself.
	AllowSubresources bool `json:"allow_subresources"`
}

// Policy derives the renderer contract from what admission decided. The caller
// chooses the redirect budget and the subresource decision; the host and the
// pinned addresses always come from the admission that already happened.
func (a Admission) Policy(maxRedirects int, allowSubresources bool) EgressPolicy {
	if maxRedirects < 0 {
		maxRedirects = 0
	}
	addresses := make([]string, len(a.Addresses))
	copy(addresses, a.Addresses)
	return EgressPolicy{Host: a.Host, Addresses: addresses,
		MaxRedirects: maxRedirects, AllowSubresources: allowSubresources}
}

// AdmitFetchURL applies the shared request-admission policy and returns the
// addresses it validated, so the renderer can pin its connections to them.
//
// Admission alone does not make a fetch safe: the renderer must enforce the
// returned policy, and VerifyEgressChain must re-check what it reports.
func AdmitFetchURL(ctx context.Context, raw string) (Admission, error) {
	return admitFetchURL(ctx, raw, net.DefaultResolver.LookupIPAddr)
}

// VerifyEgressChain re-admits every origin the renderer reports it contacted.
//
// A hop is accepted only when it passes the same admission the original target
// did, so a redirect into a private address, a rebound host, or a chain longer
// than the policy allows fails closed after the fact even though the first
// request was legitimate. An empty chain returns ErrEgressUnverified.
func VerifyEgressChain(ctx context.Context, policy EgressPolicy, chain []string) error {
	return verifyEgressChain(ctx, policy, chain, net.DefaultResolver.LookupIPAddr)
}

// admitFetchURL is AdmitFetchURL with an injectable resolver, so a test can
// assert a decision against a known DNS answer.
func admitFetchURL(ctx context.Context, raw string, lookup ipLookup) (Admission, error) {
	if err := validateFetchURL(ctx, raw, lookup); err != nil {
		return Admission{}, err
	}
	// validateFetchURL already rejected every malformed and non-public form, so
	// the remaining work is recording what it accepted.
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Admission{}, invalid("url", "malformed URL")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	port := 443
	if parsed.Scheme == schemeHTTP {
		port = 80
	}
	if explicit := parsed.Port(); explicit != "" {
		parsedPort, convErr := strconv.Atoi(explicit)
		if convErr != nil {
			return Admission{}, invalid("url", "invalid port")
		}
		port = parsedPort
	}
	addresses, err := admittedAddresses(ctx, host, lookup)
	if err != nil {
		return Admission{}, err
	}
	return Admission{URL: strings.TrimSpace(raw), Host: host, Port: port, Addresses: addresses}, nil
}

// admittedAddresses returns the canonical public addresses for a host, or an
// error when any answer is not admissible. A literal address pins itself.
func admittedAddresses(ctx context.Context, host string, lookup ipLookup) ([]string, error) {
	if ctx == nil {
		return nil, invalid("url", "context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.WithStack(err)
	}
	if literal, err := netip.ParseAddr(host); err == nil {
		if !publicAddress(literal) {
			return nil, invalid("url", "non-public addresses are not allowed")
		}
		return []string{literal.Unmap().String()}, nil
	}
	// Address capture and every reported hop need their own bound, even when
	// the caller supplies no deadline. WithTimeout preserves an earlier one.
	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resolved, err := lookup(lookupCtx, host)
	if parentErr := ctx.Err(); parentErr != nil {
		return nil, errors.WithStack(parentErr)
	}
	if lookupCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return nil, invalid("url", "hostname resolution timed out")
	}
	if err != nil {
		return nil, invalid("url", "hostname cannot be resolved")
	}
	if len(resolved) == 0 {
		return nil, invalid("url", "hostname has no addresses")
	}
	addresses := make([]string, 0, len(resolved))
	for _, address := range resolved {
		ip, ok := netip.AddrFromSlice(address.IP)
		// A single non-public answer rejects the whole name. Accepting the
		// public subset would let a split-horizon zone steer the renderer.
		if !ok || address.Zone != "" || !publicAddress(ip) {
			return nil, invalid("url", "hostname resolves to a non-public address")
		}
		addresses = append(addresses, ip.Unmap().String())
	}
	return addresses, nil
}

// verifyEgressChain is VerifyEgressChain with an injectable resolver.
func verifyEgressChain(ctx context.Context, policy EgressPolicy, chain []string, lookup ipLookup) error {
	if len(chain) == 0 {
		return ErrEgressUnverified
	}
	// The first entry is the original request; the rest are the hops it followed.
	if hops := len(chain) - 1; hops > policy.MaxRedirects {
		return errors.Errorf("renderer followed %d redirects, policy allows %d", hops, policy.MaxRedirects)
	}
	pinned := make(map[string]struct{}, len(policy.Addresses))
	for _, address := range policy.Addresses {
		pinned[address] = struct{}{}
	}
	for index, hop := range chain {
		admission, err := admitFetchURL(ctx, hop, lookup)
		if err != nil {
			return errors.Wrapf(err, "renderer contacted an inadmissible origin at hop %d", index)
		}
		if admission.Host != policy.Host {
			// A cross-origin hop is allowed only on its own admission; the
			// pinned set belongs to the originally admitted host.
			continue
		}
		for _, address := range admission.Addresses {
			if _, ok := pinned[address]; !ok {
				return errors.Errorf("hop %d resolved %s to an address outside the admitted set", index, policy.Host)
			}
		}
	}
	return nil
}

// SortedAddresses returns the policy addresses in a stable order, which keeps a
// serialized task envelope byte-comparable across retries.
func (p EgressPolicy) SortedAddresses() []string {
	addresses := make([]string, len(p.Addresses))
	copy(addresses, p.Addresses)
	sort.Strings(addresses)
	return addresses
}
