package crawleregress

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

func validReceipt() (Policy, *Receipt) {
	p := Policy{Host: "1.1.1.1", Addresses: []string{"1.1.1.1"}, MaxRedirects: 1}
	r := NewReceipt("task-1", "https://1.1.1.1/document", p)
	r.Origins = []Origin{{URL: r.TargetURL, Addresses: []string{"1.1.1.1"}, PeerAddress: "1.1.1.1", Kind: OriginDocument}}
	return p, r
}

// TestVerifyReceiptBehavior enforces bound connection evidence using synthetic public addresses only.
func TestVerifyReceiptBehavior(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Policy, *Receipt)
		want   error
	}{
		{"bounded blocked requests", func(_ *Policy, r *Receipt) { r.BlockedRequests = MaxOrigins }, nil},
		{"negative blocked requests", func(_ *Policy, r *Receipt) { r.BlockedRequests = -1 }, ErrRejected},
		{"excess blocked requests", func(_ *Policy, r *Receipt) { r.BlockedRequests = MaxOrigins + 1 }, ErrRejected},
		{"verified success", func(*Policy, *Receipt) {}, nil},
		{"negative body", func(_ *Policy, r *Receipt) { r.Origins[0].BodyBytes = -1 }, ErrRejected},
		{"response limit", func(_ *Policy, r *Receipt) { r.Origins[0].BodyBytes = MaxResponseBytes + 1 }, ErrRejected},
		{"aggregate limit", func(p *Policy, r *Receipt) {
			p.MaxRedirects = 2
			r.PolicyDigest = PolicyDigest(r.TargetURL, *p)
			r.Origins[0].BodyBytes = MaxResponseBytes
			r.Origins = append(r.Origins, r.Origins[0], r.Origins[0])
		}, ErrRejected},
		{"missing connections", func(_ *Policy, r *Receipt) { r.Origins = nil }, ErrUnverified},
		{"missing peer", func(_ *Policy, r *Receipt) { r.Origins[0].PeerAddress = "" }, ErrRejected},
		{"wrong task", func(_ *Policy, r *Receipt) { r.TaskID = "another" }, ErrRejected},
		{"wrong target", func(_ *Policy, r *Receipt) { r.TargetURL = "https://1.1.1.1/other" }, ErrRejected},
		{"wrong policy digest", func(_ *Policy, r *Receipt) { r.PolicyDigest = "forged" }, ErrRejected},
		{"wrong version", func(_ *Policy, r *Receipt) { r.Version = 0 }, ErrRejected},
		{"claimed wrong document", func(_ *Policy, r *Receipt) { r.Origins[0].URL = "https://1.1.1.1/other" }, ErrRejected},
		{"private peer", func(_ *Policy, r *Receipt) { r.Origins[0].PeerAddress = "127.0.0.1" }, ErrRejected},
		{"unpinned public peer", func(_ *Policy, r *Receipt) { r.Origins[0].PeerAddress = "8.8.8.8" }, ErrRejected},
		{"private redirect", func(_ *Policy, r *Receipt) {
			r.Origins = append(r.Origins, Origin{URL: "http://127.0.0.1/admin", Addresses: []string{"127.0.0.1"}, PeerAddress: "127.0.0.1", Kind: OriginDocument})
		}, ErrRejected},
		{"public redirect", func(_ *Policy, r *Receipt) {
			r.Origins = append(r.Origins, Origin{URL: "https://8.8.8.8/", Addresses: []string{"8.8.8.8"}, PeerAddress: "8.8.8.8", Kind: OriginDocument})
		}, nil},
		{"redirect limit", func(_ *Policy, r *Receipt) { r.Origins = append(r.Origins, r.Origins[0], r.Origins[0]) }, ErrRejected},
		{"subresource denied", func(_ *Policy, r *Receipt) {
			o := r.Origins[0]
			o.Kind = OriginSubresource
			r.Origins = append(r.Origins, o)
		}, ErrRejected},
		{"subresource permitted", func(p *Policy, r *Receipt) {
			p.AllowSubresources = true
			r.PolicyDigest = PolicyDigest(r.TargetURL, *p)
			o := r.Origins[0]
			o.Kind = OriginSubresource
			r.Origins = append(r.Origins, o)
		}, nil},
		{"private subresource", func(p *Policy, r *Receipt) {
			p.AllowSubresources = true
			r.PolicyDigest = PolicyDigest(r.TargetURL, *p)
			r.Origins = append(r.Origins, Origin{URL: "http://10.0.0.1/", Addresses: []string{"10.0.0.1"}, PeerAddress: "10.0.0.1", Kind: OriginSubresource})
		}, ErrRejected},
		{"unknown kind", func(_ *Policy, r *Receipt) { r.Origins[0].Kind = "unknown" }, ErrRejected},
		{"excessive receipts", func(_ *Policy, r *Receipt) {
			for len(r.Origins) <= 1024 {
				r.Origins = append(r.Origins, r.Origins[0])
			}
		}, ErrRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, r := validReceipt()
			tc.change(&p, r)
			err := VerifyReceipt(context.Background(), "task-1", "https://1.1.1.1/document", p, r)
			if tc.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.want)
			}
		})
	}
}

// TestPinnedReceiptDoesNotResolveDNSAgain retains safe pinned-peer evidence despite later DNS changes.
func TestPinnedReceiptDoesNotResolveDNSAgain(t *testing.T) {
	calls := 0
	a, err := AdmitURLWithResolver(context.Background(), "https://example.test/doc", func(context.Context, string) ([]net.IPAddr, error) {
		calls++
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	})
	require.NoError(t, err)
	p := a.Policy(1, false)
	r := NewReceipt("task", a.URL, p)
	r.Origins = []Origin{{URL: a.URL, Addresses: a.Addresses, PeerAddress: "1.1.1.1", Kind: OriginDocument}}
	require.NoError(t, VerifyReceipt(context.Background(), "task", a.URL, p, r))
	require.Equal(t, 2, calls, "only admission resolves, verification uses observed pinned peer")
	r.Origins[0].Addresses = []string{"8.8.8.8"}
	r.Origins[0].PeerAddress = "8.8.8.8"
	require.ErrorIs(t, VerifyReceipt(context.Background(), "task", a.URL, p, r), ErrRejected)
}

// TestReceiptCancellation preserves caller cancellation and timeout identity.
func TestReceiptCancellation(t *testing.T) {
	p, r := validReceipt()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, VerifyReceipt(ctx, "task-1", r.TargetURL, p, r), context.Canceled)
	require.ErrorIs(t, VerifyReceipt(context.Background(), "task", r.TargetURL, p, nil), ErrUnverified)
	require.Error(t, VerifyReceipt(nil, "task", r.TargetURL, p, r))
}

// TestAdmissionNegativeControls excludes rebinding and private DNS answers before connection.
func TestAdmissionNegativeControls(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "::1", "169.254.169.254"} {
		_, err := AdmitURLWithResolver(context.Background(), "https://example.test/", func(context.Context, string) ([]net.IPAddr, error) { return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil })
		require.Error(t, err)
	}
	count := 0
	_, err := AdmitURLWithResolver(context.Background(), "https://example.test/", func(context.Context, string) ([]net.IPAddr, error) {
		count++
		ip := "1.1.1.1"
		if count > 1 {
			ip = "127.0.0.1"
		}
		return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
	})
	require.Error(t, err, "DNS rebinding between validation and capture must be rejected")
	_, err = AdmitURLWithResolver(context.Background(), "https://example.test/", func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.WithStack(context.DeadlineExceeded)
	})
	require.Error(t, err)
}

// TestReceiptWireAndNormalization preserves connection evidence and HTTP request identity.
func TestReceiptWireAndNormalization(t *testing.T) {
	p, r := validReceipt()
	raw, err := json.Marshal(r)
	require.NoError(t, err)
	var decoded Receipt
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, *r, decoded)
	target := "https://1.1.1.1#fragment"
	r = NewReceipt("task", target, p)
	r.Origins = []Origin{{URL: "https://1.1.1.1/", Addresses: p.Addresses, PeerAddress: "1.1.1.1", Kind: OriginDocument}}
	require.NoError(t, VerifyReceipt(context.Background(), "task", target, p, r))
	require.NotEqual(t, PolicyDigest("https://1.1.1.1/a", p), PolicyDigest("https://1.1.1.1/b", p))
	require.NotEqual(t, RequestURL("https://1.1.1.1/a?q=1"), RequestURL("https://1.1.1.1/a?q=2"))
}
