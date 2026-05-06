package pageindex

import (
	"sync"
	"testing"
)

func TestBudgetTake(t *testing.T) {
	b := NewBudget(100)
	if !b.Take(40) {
		t.Fatal("take 40 should succeed")
	}
	if !b.Take(60) {
		t.Fatal("take 60 should succeed")
	}
	if b.Take(1) {
		t.Fatal("budget exhausted")
	}
}

func TestBudgetConcurrentSafety(t *testing.T) {
	b := NewBudget(10000)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Take(100)
		}()
	}
	wg.Wait()
	if b.Remaining() != 0 {
		t.Fatalf("expected 0, got %d", b.Remaining())
	}
}
