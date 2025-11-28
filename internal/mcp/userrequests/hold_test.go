package userrequests

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestHoldManager_SetHold(t *testing.T) {
	t.Parallel()

	mgr := NewHoldManager(nil, nil, nil)
	apiKeyHash := "test-api-key-hash"

	state := mgr.SetHold(apiKeyHash)
	require.True(t, state.Active)
	// ExpiresAt should be zero until an agent waits
	require.True(t, state.ExpiresAt.IsZero())
	require.False(t, state.Waiting)
}

func TestHoldManager_GetHoldState(t *testing.T) {
	t.Parallel()

	mgr := NewHoldManager(nil, nil, nil)
	apiKeyHash := "test-api-key-hash-2"

	// Initially no hold
	state := mgr.GetHoldState(apiKeyHash)
	require.False(t, state.Active)

	// Set hold
	mgr.SetHold(apiKeyHash)
	state = mgr.GetHoldState(apiKeyHash)
	require.True(t, state.Active)
	require.False(t, state.Waiting)
}

func TestHoldManager_ReleaseHold(t *testing.T) {
	t.Parallel()

	mgr := NewHoldManager(nil, nil, nil)
	apiKeyHash := "test-api-key-hash-3"

	// Set then release
	mgr.SetHold(apiKeyHash)
	require.True(t, mgr.IsHoldActive(apiKeyHash))

	mgr.ReleaseHold(apiKeyHash)
	require.False(t, mgr.IsHoldActive(apiKeyHash))
}

func TestHoldManager_SubmitCommand_NotifiesWaiter(t *testing.T) {
	t.Parallel()

	mgr := NewHoldManager(nil, nil, nil)
	apiKeyHash := "test-api-key-hash-4"

	mgr.SetHold(apiKeyHash)

	request := &Request{
		ID:      uuid.New(),
		Content: "Test command",
	}

	var wg sync.WaitGroup
	var received *Request

	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		received = mgr.WaitForCommand(ctx, apiKeyHash)
	}()

	// Give goroutine time to start waiting
	time.Sleep(100 * time.Millisecond)

	// Submit command
	mgr.SubmitCommand(context.Background(), apiKeyHash, request)

	wg.Wait()
	require.NotNil(t, received)
	require.Equal(t, request.ID, received.ID)
	require.Equal(t, "Test command", received.Content)

	// Hold should be released after submission
	require.False(t, mgr.IsHoldActive(apiKeyHash))
}

func TestHoldManager_WaitForCommand_ReturnsNilWhenNoHold(t *testing.T) {
	t.Parallel()

	mgr := NewHoldManager(nil, nil, nil)
	apiKeyHash := "test-api-key-hash-5"

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result := mgr.WaitForCommand(ctx, apiKeyHash)
	require.Nil(t, result)
}

func TestHoldManager_WaitStartsTimer(t *testing.T) {
	t.Parallel()

	// Use a custom clock
	now := time.Now().UTC()
	mockClock := func() time.Time { return now }

	mgr := NewHoldManager(nil, nil, mockClock)
	apiKeyHash := "test-api-key-hash-6"

	// Set hold - no expiration initially
	state := mgr.SetHold(apiKeyHash)
	require.True(t, state.Active)
	require.True(t, state.ExpiresAt.IsZero())
	require.False(t, state.Waiting)

	// Start a wait in a goroutine
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = mgr.WaitForCommand(ctx, apiKeyHash)
	}()

	// Give goroutine time to call WaitForCommand
	time.Sleep(50 * time.Millisecond)

	// Now check state - should have expiration set and waiting=true
	state = mgr.GetHoldState(apiKeyHash)
	require.True(t, state.Active)
	require.True(t, state.Waiting)
	require.Equal(t, now.Add(HoldMaxDuration), state.ExpiresAt)
}

func TestHoldManager_ResetHold(t *testing.T) {
	t.Parallel()

	mgr := NewHoldManager(nil, nil, nil)
	apiKeyHash := "test-api-key-hash-7"

	// Set initial hold
	state1 := mgr.SetHold(apiKeyHash)
	require.True(t, state1.Active)
	require.True(t, state1.ExpiresAt.IsZero()) // No expiration until agent waits

	// Reset hold
	state2 := mgr.SetHold(apiKeyHash)
	require.True(t, state2.Active)
	require.True(t, state2.ExpiresAt.IsZero()) // Still no expiration
}
