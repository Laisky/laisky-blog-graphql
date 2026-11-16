package httpbody

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// sendBurstThen writes burst bytes at once, then follows with tail, a function
// that keeps (or stops) delivering on the same connection.
func sendBurstThen(t *testing.T, address string, total, burst int, tail func(net.Conn)) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: %d\r\n\r\n%s", total, strings.Repeat("x", burst))
	require.NoError(t, err)
	go tail(conn)
}

// awaitOutcome waits for the handler's verdict and how long it took.
func awaitOutcome(t *testing.T, outcomes <-chan string, started time.Time, within time.Duration) (string, time.Duration) {
	t.Helper()
	select {
	case outcome := <-outcomes:
		return outcome, time.Since(started)
	case <-time.After(within):
		return "no outcome", time.Since(started)
	}
}

// TestBurstThenStallIsCutAfterTheIdleBound reproduces the review's
// burst-then-stall case: 64 KiB delivered at once would earn 64 s of read
// time at 1 KiB/s if credit were unbounded. Delivered bytes may earn at most
// MaxIdle (here Grace, the default) of read time ahead, so the stalled body
// is cut shortly after it stops.
func TestBurstThenStallIsCutAfterTheIdleBound(t *testing.T) {
	const grace = 200 * time.Millisecond
	server, outcomes := readOutcome(t, Budget{MaxBytes: 1 << 20, MinBytesPerSecond: 1024, Grace: grace})
	started := time.Now()
	sendBurstThen(t, server.Listener.Addr().String(), 1<<20, 64<<10, func(net.Conn) {})

	outcome, elapsed := awaitOutcome(t, outcomes, started, 5*time.Second)
	require.Equal(t, "cut", outcome, "a body that stalls after a burst must not keep its connection for the burst's whole credit")
	require.Less(t, elapsed, grace+time.Second)
}

// TestBurstThenTrickleIsCutAfterTheIdleBound covers the variant that a plain
// inter-read idle timer (nginx client_body_timeout) would admit: after the
// burst the client sends a few bytes just often enough to never look idle,
// far below the minimum rate. Bounded credit drains while the trickle adds
// almost none, so the body is cut soon after the bound.
func TestBurstThenTrickleIsCutAfterTheIdleBound(t *testing.T) {
	const grace = 200 * time.Millisecond
	server, outcomes := readOutcome(t, Budget{MaxBytes: 1 << 20, MinBytesPerSecond: 1024, Grace: grace, MaxIdle: 400 * time.Millisecond})
	started := time.Now()
	sendBurstThen(t, server.Listener.Addr().String(), 1<<20, 64<<10, func(conn net.Conn) {
		for range 100 {
			time.Sleep(100 * time.Millisecond)
			if _, err := conn.Write([]byte("12345678")); err != nil {
				return
			}
		}
	})

	outcome, elapsed := awaitOutcome(t, outcomes, started, 5*time.Second)
	require.Equal(t, "cut", outcome, "a trickle far below the minimum rate must not live on a burst's credit")
	require.Less(t, elapsed, 400*time.Millisecond+time.Second)
}

// TestPauseWithinTheIdleBoundIsAdmitted keeps legitimate fast clients working:
// a pause shorter than MaxIdle between two fast halves of a body is fine, even
// though it is longer than Grace.
func TestPauseWithinTheIdleBoundIsAdmitted(t *testing.T) {
	const half = 32 << 10
	server, outcomes := readOutcome(t, Budget{MaxBytes: 1 << 20, MinBytesPerSecond: 1024, Grace: 200 * time.Millisecond, MaxIdle: 900 * time.Millisecond})
	started := time.Now()
	sendBurstThen(t, server.Listener.Addr().String(), 2*half, half, func(conn net.Conn) {
		time.Sleep(450 * time.Millisecond)
		_, _ = conn.Write([]byte(strings.Repeat("y", half)))
	})

	outcome, _ := awaitOutcome(t, outcomes, started, 5*time.Second)
	require.Equal(t, "complete", outcome)
}

// recordingSetter records the read deadlines armed on the connection.
type recordingSetter struct {
	armed []time.Time
}

// SetReadDeadline records deadline.
func (s *recordingSetter) SetReadDeadline(deadline time.Time) error {
	s.armed = append(s.armed, deadline)
	return nil
}

// TestReadCreditIsBoundedAndDrains checks the token-bucket arithmetic on a
// fake clock: a burst banks at most MaxIdle, a later trickle cannot move the
// armed deadline, a steady client at the minimum rate keeps its margin, and
// the absolute cap holds.
func TestReadCreditIsBoundedAndDrains(t *testing.T) {
	start := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	clock := start
	at := func(offset time.Duration) time.Time { return start.Add(offset) }
	setter := &recordingSetter{}
	budget := Budget{MaxBytes: 1 << 20, MinBytesPerSecond: 1000, Grace: time.Second, MaxIdle: 4 * time.Second, MaxDuration: 20 * time.Second}
	body := newDeadlineBody(nil, setter, budget, func() time.Time { return clock })
	require.Equal(t, at(time.Second), body.armed, "before any byte the client has the grace period")

	clock = at(500 * time.Millisecond)
	body.extend(100_000) // 100 s of credit at the minimum rate
	require.Equal(t, at(4500*time.Millisecond), body.deadline, "a burst banks at most MaxIdle ahead of its delivery")
	require.Equal(t, body.deadline, body.armed)

	clock = at(4 * time.Second)
	body.extend(8)
	require.Equal(t, at(4508*time.Millisecond), body.deadline, "a trickle earns only its own bytes")
	require.Equal(t, at(4500*time.Millisecond), body.armed, "and does not re-arm the connection for a few milliseconds")

	for second := 5; second <= 30; second++ {
		clock = at(time.Duration(second)*time.Second - 700*time.Millisecond)
		body.extend(1000)
	}
	require.Equal(t, at(20*time.Second), body.deadline, "the absolute cap bounds even a client above the minimum rate")
	require.LessOrEqual(t, len(setter.armed), 20, "the connection deadline moves in steps, not on every read")
	for i := 1; i < len(setter.armed); i++ {
		require.True(t, setter.armed[i].After(setter.armed[i-1]), "armed deadlines only move forward")
	}
}

// TestMaxIdleNeverUndercutsGrace keeps the first-byte allowance: a credit
// bound below Grace would cut a client that sent its first bytes early.
func TestMaxIdleNeverUndercutsGrace(t *testing.T) {
	require.Equal(t, 3*time.Second, Budget{Grace: 3 * time.Second}.maxIdle())
	require.Equal(t, 3*time.Second, Budget{Grace: 3 * time.Second, MaxIdle: time.Second}.maxIdle())
	require.Equal(t, 30*time.Second, Budget{Grace: 3 * time.Second, MaxIdle: 30 * time.Second}.maxIdle())
}
