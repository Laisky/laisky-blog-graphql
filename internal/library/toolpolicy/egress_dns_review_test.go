package toolpolicy

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
)

// TestAdmissionBoundsEveryDNSLookup covers both the initial validator lookup
// and the second address-capture lookup that previously had no deadline.
func TestAdmissionBoundsEveryDNSLookup(t *testing.T) {
	var observed []context.Context
	lookup := func(ctx context.Context, _ string) ([]net.IPAddr, error) {
		observed = append(observed, ctx)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second || time.Until(deadline) <= 0 {
			t.Errorf("lookup %d is not bounded by an active <=10s deadline", len(observed))
		}
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	admission, err := admitFetchURL(context.Background(), "https://example.test/document", lookup)
	if err != nil || len(admission.Addresses) != 1 || admission.Addresses[0] != "1.1.1.1" {
		t.Fatalf("valid admission changed: %+v, %v", admission, err)
	}
	if len(observed) != 2 {
		t.Fatalf("expected both validation and address capture, got %d lookups", len(observed))
	}
	for i, ctx := range observed {
		if ctx.Err() == nil {
			t.Errorf("lookup %d child context was not released after admission", i+1)
		}
	}
}

// TestReportedChainBoundsEveryDNSLookup prevents a later redirect from escaping
// the same DNS budget used for the original admitted request.
func TestReportedChainBoundsEveryDNSLookup(t *testing.T) {
	calls := 0
	lookup := func(ctx context.Context, _ string) ([]net.IPAddr, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second {
			t.Errorf("reported-chain lookup %d has no application-enforced budget", calls)
		}
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	policy := EgressPolicy{Host: "example.test", Addresses: []string{"1.1.1.1"}, MaxRedirects: 1}
	if err := verifyEgressChain(context.Background(), policy,
		[]string{"https://example.test/start", "https://example.test/final"}, lookup); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("expected two lookups for each of two hops, got %d", calls)
	}
}

// TestAdmittedAddressesHonorsParentContext verifies cancellation is not hidden
// as an unresolved hostname, and an earlier caller deadline is never extended.
func TestAdmittedAddressesHonorsParentContext(t *testing.T) {
	t.Run("pre-cancelled caller never starts lookup", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		_, err := admittedAddresses(ctx, "example.test", func(context.Context, string) ([]net.IPAddr, error) {
			calls++
			return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
		})
		if !errors.Is(err, context.Canceled) || calls != 0 {
			t.Fatalf("err=%v lookup calls=%d", err, calls)
		}
	})
	t.Run("earlier deadline is inherited and child is released", func(t *testing.T) {
		want := time.Now().Add(time.Second)
		ctx, cancel := context.WithDeadline(context.Background(), want)
		defer cancel()
		var child context.Context
		_, err := admittedAddresses(ctx, "example.test", func(got context.Context, _ string) ([]net.IPAddr, error) {
			child = got
			deadline, ok := got.Deadline()
			if !ok || !deadline.Equal(want) {
				t.Errorf("parent deadline was changed: %v", deadline)
			}
			return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
		})
		if err != nil || ctx.Err() != nil || child == nil || child.Err() == nil {
			t.Fatalf("err=%v parent=%v child=%v", err, ctx.Err(), child)
		}
	})
	t.Run("cancellation during lookup is preserved", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err := admittedAddresses(ctx, "example.test", func(got context.Context, _ string) ([]net.IPAddr, error) {
			cancel()
			return nil, got.Err()
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller cancellation lost: %v", err)
		}
	})
	t.Run("expired parent remains deadline exceeded", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		calls := 0
		_, err := admittedAddresses(ctx, "example.test", func(context.Context, string) ([]net.IPAddr, error) {
			calls++
			return nil, errors.New("should not run")
		})
		if !errors.Is(err, context.DeadlineExceeded) || calls != 0 {
			t.Fatalf("expired caller: err=%v calls=%d", err, calls)
		}
	})
}

// TestAdmittedAddressesSeparatesTimeoutFromDNSFailure uses controlled resolver
// failures, avoiding real DNS traffic and a ten-second sleep in normal tests.
func TestAdmittedAddressesSeparatesTimeoutFromDNSFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		want    string
	}{
		{"resolver deadline", context.DeadlineExceeded, "url hostname resolution timed out"},
		{"unresolved host", errors.New("synthetic-secret resolver detail"), "url hostname cannot be resolved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := admittedAddresses(context.Background(), "example.test", func(context.Context, string) ([]net.IPAddr, error) {
				return nil, tc.failure
			})
			if err == nil || err.Error() != tc.want || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("unexpected public error: %v", err)
			}
		})
	}
}
