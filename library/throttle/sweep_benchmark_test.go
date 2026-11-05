package throttle

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// BenchmarkSweepRetainedTitles measures a complete scan of active, non-full
// buckets. It is deliberately manual rather than part of the fast CI gate.
func BenchmarkSweepRetainedTitles(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("titles=%d", count), func(b *testing.B) {
			th, err := NewTelegramThrottle(context.Background(), &TelegramThrottleCfg{
				TotalNPerSec: count + 1, TotalBurst: count + 1,
				EachTitleNPerSec: 1, EachTitleBurst: 2,
			})
			if err != nil {
				b.Fatal(err)
			}
			now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
			th.now = func() time.Time { return now }
			th.total.last = now
			for i := range count {
				if !th.Allow(fmt.Sprintf("title-%d", i)) {
					b.Fatal("failed to populate buckets")
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				th.mu.Lock()
				th.lastSweep = now.Add(-th.sweepEvery)
				th.sweepLocked(now)
				th.mu.Unlock()
			}
		})
	}
}

// BenchmarkAdmissionDuringSweep measures worst-case contention with a sweep on
// every admission. The frozen clock keeps all pre-populated buckets active.
func BenchmarkAdmissionDuringSweep(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("titles=%d", count), func(b *testing.B) {
			th, err := NewTelegramThrottle(context.Background(), &TelegramThrottleCfg{
				TotalNPerSec: count + 1, TotalBurst: count + 1,
				EachTitleNPerSec: 1, EachTitleBurst: 2,
			})
			if err != nil {
				b.Fatal(err)
			}
			now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
			th.now = func() time.Time { return now }
			th.total.last = now
			for i := range count {
				if !th.Allow(fmt.Sprintf("title-%d", i)) {
					b.Fatal("failed to populate buckets")
				}
			}
			th.sweepEvery = 0
			th.lastSweep = now
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					th.Allow("title-0")
				}
			})
		})
	}
}
