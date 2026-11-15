package controller

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// nearFullTracker returns a tracker whose account table is two records below
// its default bound, filled with evictable spellings (one failure each), half
// of them linked to a store identity, so every success scans a full table and
// every new account forces an eviction sweep.
func nearFullTracker(b *testing.B) *authChallengeTracker {
	b.Helper()
	tracker := newAuthChallengeTracker(time.Minute, 1<<30, 15*time.Minute, 3, defaultTurnstileMaxTrackedClients)
	for i := 0; len(tracker.accounts) < tracker.maxAccounts-2; i++ {
		identity := ""
		if i%2 == 0 {
			identity = tracker.identityKey(fmt.Sprintf("mongo:%d", i))
		}
		tracker.recordFailureFor(tracker.subject(benchClient(i), fmt.Sprintf("user-%d@example.test", i)), identity)
	}
	return tracker
}

// benchClient returns a distinct client key for i.
func benchClient(i int) string {
	return fmt.Sprintf("198.18.%d.%d", i/256%256, i%256)
}

// BenchmarkAuthChallengeTrackerNearFullTable measures the tracker paths that
// hold its mutex while scanning or sorting the account table, with the table
// at its default bound of 10000 records (review P3 "global mutex and
// bounded-table scans"): a failed then successful sign-in (the success scans
// the table), a failure for a new account (insertion with eviction), and a
// parallel mix of admissions, failures and successes from many goroutines.
func BenchmarkAuthChallengeTrackerNearFullTable(b *testing.B) {
	b.Run("failure_then_success", func(b *testing.B) {
		tracker := nearFullTracker(b)
		subject := tracker.subject("192.0.2.1", "owner@example.test")
		identity := tracker.identityKey("mongo:owner")
		b.ResetTimer()
		for range b.N {
			tracker.recordFailureFor(subject, identity)
			tracker.recordSuccessFor(subject, identity)
		}
	})
	b.Run("failure_new_account", func(b *testing.B) {
		tracker := nearFullTracker(b)
		b.ResetTimer()
		for i := range b.N {
			tracker.recordFailureFor(tracker.subject(benchClient(i), fmt.Sprintf("new-%d@example.test", i)), "")
		}
	})
	b.Run("parallel_mixed", func(b *testing.B) {
		tracker := nearFullTracker(b)
		var seq atomic.Int64
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				i := int(seq.Add(1))
				subject := tracker.subject(benchClient(i), fmt.Sprintf("mixed-%d@example.test", i%2048))
				decision := tracker.admit(subject)
				if decision.admitted {
					if i%4 == 0 {
						tracker.recordSuccessFor(subject, tracker.identityKey(fmt.Sprintf("mongo:mixed-%d", i%2048)))
					} else {
						tracker.recordFailureFor(subject, tracker.identityKey(fmt.Sprintf("mongo:mixed-%d", i%2048)))
					}
				}
				tracker.release(decision.reservation)
			}
		})
	})
}
