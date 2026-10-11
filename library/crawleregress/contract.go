// Package crawleregress publishes the producer/renderer pinned-egress contract.
// Receipts are trusted-worker audit evidence, not cryptographic attestation.
package crawleregress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
)

// Policy is the admitted connection policy carried with a crawl task.
type Policy = toolpolicy.EgressPolicy

// Admission records the exact public addresses admitted for one destination.
type Admission = toolpolicy.Admission

// ReceiptVersion is the supported connection-evidence schema version.
const ReceiptVersion = 1

// DefaultMaxRedirects is the ordinary producer redirect budget.
const DefaultMaxRedirects = 5

// MaxRedirects bounds policy admission and evidence verification work.
const MaxRedirects = 20

// MaxOrigins bounds aggregate document and subresource connections per crawl.
const MaxOrigins = 1024

// MaxResponseBytes bounds each fetched document or subresource body.
const MaxResponseBytes = 16 << 20

// MaxTotalBytes bounds aggregate fetched bodies per crawl.
const MaxTotalBytes = 32 << 20

// OriginDocument identifies the main document and its redirect destinations.
const OriginDocument = "document"

// OriginSubresource identifies an independently admitted page subresource.
const OriginSubresource = "subresource"

// ErrUnverified indicates missing connection evidence rather than a proven breach.
var ErrUnverified = toolpolicy.ErrEgressUnverified

// ErrRejected indicates mismatched or invalid connection evidence.
var ErrRejected = errors.New("renderer connection evidence rejected")

// Origin records a contacted URL, its admitted addresses and the actual connected peer.
// URL is audit data and must never become an alert field.
type Origin struct {
	URL         string   `json:"url"`
	Addresses   []string `json:"addresses"`
	PeerAddress string   `json:"peer_address"`
	Kind        string   `json:"kind"`
	// BodyBytes is the observed body length; zero is valid for empty responses.
	BodyBytes int64 `json:"body_bytes"`
}

// Receipt binds observed connections to the exact submitted task and policy.
// A compromised trusted worker can fabricate a receipt; this is not attestation.
type Receipt struct {
	Version      int      `json:"version"`
	TaskID       string   `json:"task_id"`
	TargetURL    string   `json:"target_url"`
	PolicyDigest string   `json:"policy_digest"`
	Origins      []Origin `json:"origins"`
	// BlockedRequests counts denied browser requests that made no upstream contact.
	BlockedRequests int64 `json:"blocked_requests"`
}

// AdmitURL validates and resolves a public HTTP(S) target for connection pinning.
func AdmitURL(ctx context.Context, raw string) (Admission, error) {
	return toolpolicy.AdmitFetchURL(ctx, raw)
}

// AdmitURLWithResolver validates a target using a supplied resolver.
func AdmitURLWithResolver(ctx context.Context, raw string, lookup func(context.Context, string) ([]net.IPAddr, error)) (Admission, error) {
	return toolpolicy.AdmitFetchURLWithResolver(ctx, raw, lookup)
}

// ValidateDestination validates previously admitted addresses without re-resolving DNS.
func ValidateDestination(ctx context.Context, raw string, addresses []string) (Admission, error) {
	return toolpolicy.ValidatePinnedDestination(ctx, raw, addresses)
}

// PolicyDigest binds a policy and exact target URL independent of address order.
func PolicyDigest(target string, policy Policy) string {
	policy.Addresses = policy.SortedAddresses()
	payload, err := json.Marshal(struct {
		Version int    `json:"version"`
		Target  string `json:"target"`
		Policy  Policy `json:"policy"`
	}{ReceiptVersion, target, policy})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// NewReceipt creates an empty receipt bound to a submitted task and policy.
func NewReceipt(taskID, target string, policy Policy) *Receipt {
	return &Receipt{Version: ReceiptVersion, TaskID: taskID, TargetURL: target, PolicyDigest: PolicyDigest(target, policy)}
}

// VerifyReceipt validates task binding, redirect/subresource decisions and pinned public peers.
// It never resolves DNS again: safe pinned connections remain valid after DNS changes.
func VerifyReceipt(ctx context.Context, taskID, target string, policy Policy, receipt *Receipt) error {
	if ctx == nil {
		return errors.Wrap(ErrRejected, "context required")
	}
	if err := ctx.Err(); err != nil {
		return errors.WithStack(err)
	}
	if receipt == nil || len(receipt.Origins) == 0 {
		return errors.WithStack(ErrUnverified)
	}
	if receipt.Version != ReceiptVersion || receipt.TaskID != taskID || receipt.TargetURL != target || receipt.PolicyDigest != PolicyDigest(target, policy) {
		return errors.Wrap(ErrRejected, "task or policy binding mismatch")
	}
	initial, err := ValidateDestination(ctx, target, policy.Addresses)
	if err != nil || initial.Host != policy.Host || policy.MaxRedirects < 0 || policy.MaxRedirects > MaxRedirects {
		return errors.Wrap(ErrRejected, "invalid submitted policy")
	}
	if receipt.BlockedRequests < 0 || receipt.BlockedRequests > MaxOrigins {
		return errors.Wrap(ErrRejected, "blocked request budget exceeded")
	}
	if len(receipt.Origins) > MaxOrigins {
		return errors.Wrap(ErrRejected, "too many origins")
	}
	documents := 0
	var totalBytes int64
	for index, origin := range receipt.Origins {
		if origin.BodyBytes < 0 || origin.BodyBytes > MaxResponseBytes || totalBytes > MaxTotalBytes-origin.BodyBytes {
			return errors.Wrap(ErrRejected, "body budget exceeded")
		}
		totalBytes += origin.BodyBytes
		if err := ctx.Err(); err != nil {
			return errors.WithStack(err)
		}
		switch origin.Kind {
		case OriginDocument:
			if documents == 0 && (index != 0 || RequestURL(origin.URL) != RequestURL(target)) {
				return errors.Wrap(ErrRejected, "initial document mismatch")
			}
			documents++
			if documents-1 > policy.MaxRedirects {
				return errors.Wrap(ErrRejected, "redirect budget exceeded")
			}
		case OriginSubresource:
			if documents == 0 || !policy.AllowSubresources {
				return errors.Wrap(ErrRejected, "subresources forbidden")
			}
		default:
			return errors.Wrap(ErrRejected, "unknown origin kind")
		}
		if err := verifyOrigin(ctx, policy, origin); err != nil {
			return err
		}
	}
	if documents == 0 {
		return errors.Wrap(ErrRejected, "missing initial document")
	}
	return nil
}

// ClonePolicy returns a policy with independently owned canonical address storage.
func ClonePolicy(policy Policy) Policy { policy.Addresses = policy.SortedAddresses(); return policy }

func containsAddress(addresses []string, peer netip.Addr) bool {
	for _, address := range addresses {
		if ip, err := netip.ParseAddr(address); err == nil && ip.Unmap() == peer.Unmap() {
			return true
		}
	}
	return false
}

// RequestURL canonicalizes the HTTP request target without changing its path or query.
// Browser normalization drops fragments and adds the root path for a bare host.
// Invalid inputs return an empty string and must still pass ValidateDestination.
func RequestURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := parsed.Port()
	if port != "" && (parsed.Scheme != "http" || port != "80") && (parsed.Scheme != "https" || port != "443") {
		host += ":" + port
	}
	parsed.Host = host
	parsed.Fragment, parsed.RawFragment = "", ""
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String()
}

// ValidatePolicy checks a task target and its immutable, public connection policy.
func ValidatePolicy(ctx context.Context, target string, policy Policy) (Admission, error) {
	admission, err := ValidateDestination(ctx, target, policy.Addresses)
	if err != nil {
		return Admission{}, errors.Wrap(err, "validate crawler policy")
	}
	if admission.Host != policy.Host || policy.MaxRedirects < 0 || policy.MaxRedirects > MaxRedirects {
		return Admission{}, errors.Wrap(ErrRejected, "invalid submitted policy")
	}
	return admission, nil
}

// verifyOrigin checks one observed connection without DNS or raw-value errors.
func verifyOrigin(ctx context.Context, policy Policy, origin Origin) error {
	admission, err := ValidateDestination(ctx, origin.URL, origin.Addresses)
	if err != nil {
		return errors.Wrap(ErrRejected, "invalid origin admission")
	}
	peer, err := netip.ParseAddr(origin.PeerAddress)
	if err != nil || peer.Zone() != "" || !containsAddress(admission.Addresses, peer) {
		return errors.Wrap(ErrRejected, "peer outside admitted addresses")
	}
	if admission.Host != policy.Host {
		return nil
	}
	if !containsAddress(policy.Addresses, peer) {
		return errors.Wrap(ErrRejected, "peer outside submitted pins")
	}
	for _, address := range admission.Addresses {
		ip, parseErr := netip.ParseAddr(address)
		if parseErr != nil || !containsAddress(policy.Addresses, ip) {
			return errors.Wrap(ErrRejected, "origin changed submitted pins")
		}
	}
	return nil
}
