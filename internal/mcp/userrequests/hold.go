package userrequests

import (
	"context"
	"sync"
	"time"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

const (
	// HoldMaxDuration is the maximum time a hold can remain active after an agent connects.
	// This timeout exists to prevent the agent connection from timing out.
	// Before an agent connects, the hold remains indefinitely.
	HoldMaxDuration = 20 * time.Second
)

// HoldState describes the current state of a hold for a given user.
type HoldState struct {
	Active    bool      `json:"active"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	// Waiting indicates whether an agent is currently waiting for a command.
	Waiting bool `json:"waiting"`
}

// holdEntry tracks a single user's hold state.
type holdEntry struct {
	// expiresAt is zero until an agent starts waiting, then set to clock + HoldMaxDuration.
	expiresAt time.Time
	// submitted is closed when a command is submitted or hold expires/released.
	// When a command is submitted, the request is written before closing.
	submitted chan *Request
	// cancel cancels the expiration goroutine (if running).
	cancel context.CancelFunc
	// waiting indicates if an agent is currently waiting.
	waiting bool
	// timedOut indicates whether the hold ended due to the timeout elapsing.
	timedOut bool
}

// HoldManager coordinates hold states for multiple users.
// Hold has two phases:
// 1. Activated but no agent waiting: hold remains indefinitely
// 2. Agent connects and waits: 20s countdown begins to prevent agent timeout
type HoldManager struct {
	mu      sync.Mutex
	holds   map[string]*holdEntry // key is APIKeyHash
	logger  logSDK.Logger
	clock   Clock
	service *Service
}

// NewHoldManager constructs a HoldManager with the given service and optional logger/clock.
func NewHoldManager(service *Service, logger logSDK.Logger, clock Clock) *HoldManager {
	if logger == nil {
		logger = log.Logger.Named("hold_manager")
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &HoldManager{
		holds:   make(map[string]*holdEntry),
		logger:  logger,
		clock:   clock,
		service: service,
	}
}

// SetHold activates a hold for the given user (identified by APIKeyHash).
// The hold remains indefinitely until an agent connects and starts waiting.
// If a hold is already active, it resets the state.
// Returns the new hold state.
func (m *HoldManager) SetHold(apiKeyHash string) HoldState {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Cancel any existing hold
	if existing, ok := m.holds[apiKeyHash]; ok {
		if existing.cancel != nil {
			existing.cancel()
		}
		close(existing.submitted)
	}

	entry := &holdEntry{
		expiresAt: time.Time{}, // Zero - no expiration until agent waits
		submitted: make(chan *Request, 1),
		cancel:    nil,
		waiting:   false,
		timedOut:  false,
	}
	m.holds[apiKeyHash] = entry

	m.log().Info("hold activated (indefinite until agent connects)",
		zap.String("api_key_hash", apiKeyHash),
	)

	return HoldState{
		Active:  true,
		Waiting: false,
	}
}

// ReleaseHold deactivates the hold for the given user without submitting a command.
func (m *HoldManager) ReleaseHold(apiKeyHash string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if entry, ok := m.holds[apiKeyHash]; ok {
		entry.timedOut = false
		if entry.cancel != nil {
			entry.cancel()
		}
		close(entry.submitted)
		delete(m.holds, apiKeyHash)
		m.log().Info("hold released", zap.String("api_key_hash", apiKeyHash))
	}
}

// GetHoldState returns the current hold state for the given user.
func (m *HoldManager) GetHoldState(apiKeyHash string) HoldState {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, ok := m.holds[apiKeyHash]
	if !ok {
		return HoldState{Active: false}
	}

	// If expiration is set and passed, hold has expired
	if !entry.expiresAt.IsZero() && m.clock().After(entry.expiresAt) {
		return HoldState{Active: false}
	}

	return HoldState{
		Active:    true,
		ExpiresAt: entry.expiresAt,
		Waiting:   entry.waiting,
	}
}

// SubmitCommand notifies any waiting consumers about a new user request.
// This automatically releases the hold.
// Returns true if the command was sent to a waiting agent, false otherwise.
func (m *HoldManager) SubmitCommand(ctx context.Context, apiKeyHash string, request *Request) bool {
	m.mu.Lock()
	entry, ok := m.holds[apiKeyHash]
	wasWaiting := ok && entry != nil && entry.waiting
	if ok {
		delete(m.holds, apiKeyHash)
	}
	m.mu.Unlock()

	if ok && entry != nil {
		entry.timedOut = false
		// Notify waiting consumer
		select {
		case entry.submitted <- request:
		default:
		}
		if entry.cancel != nil {
			entry.cancel()
		}
		close(entry.submitted)
		m.log().Info("command submitted during hold",
			zap.String("api_key_hash", apiKeyHash),
			zap.String("request_id", request.ID.String()),
			zap.Bool("was_waiting", wasWaiting),
		)
	}

	return wasWaiting
}

// WaitForCommand blocks until a command is submitted or the hold expires.
// When called, this starts the expiration timer (20s) to prevent agent timeout.
// Returns the submitted request if one arrives, or nil along with whether the hold timed out.
// If no hold is active, returns immediately with nil and false.
func (m *HoldManager) WaitForCommand(ctx context.Context, apiKeyHash string) (*Request, bool) {
	m.mu.Lock()
	entry, ok := m.holds[apiKeyHash]
	if !ok {
		m.mu.Unlock()
		return nil, false
	}

	// Start the expiration timer when agent begins waiting
	if !entry.waiting {
		entry.waiting = true
		entry.expiresAt = m.clock().Add(HoldMaxDuration)

		expireCtx, cancel := context.WithCancel(context.Background())
		entry.cancel = cancel

		m.log().Info("agent connected, hold timer started",
			zap.String("api_key_hash", apiKeyHash),
			zap.Time("expires_at", entry.expiresAt),
		)

		// Start expiration goroutine
		go m.expireHold(expireCtx, apiKeyHash, entry.expiresAt)
	}
	m.mu.Unlock()

	select {
	case req, ok := <-entry.submitted:
		if !ok {
			return nil, entry.timedOut
		}
		return req, false
	case <-ctx.Done():
		return nil, false
	}
}

// IsHoldActive returns true if a hold is currently active for the given user.
func (m *HoldManager) IsHoldActive(apiKeyHash string) bool {
	state := m.GetHoldState(apiKeyHash)
	return state.Active
}

func (m *HoldManager) expireHold(ctx context.Context, apiKeyHash string, expiresAt time.Time) {
	waitDuration := time.Until(expiresAt)
	if waitDuration <= 0 {
		waitDuration = time.Millisecond
	}

	timer := time.NewTimer(waitDuration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		// Hold was released or command was submitted
		return
	case <-timer.C:
		m.mu.Lock()
		entry, ok := m.holds[apiKeyHash]
		if ok && !entry.expiresAt.IsZero() && entry.expiresAt.Equal(expiresAt) {
			entry.timedOut = true
			delete(m.holds, apiKeyHash)
			if entry.cancel != nil {
				entry.cancel()
			}
			close(entry.submitted)
			m.log().Info("hold expired (agent timeout)", zap.String("api_key_hash", apiKeyHash))
		}
		m.mu.Unlock()
	}
}

func (m *HoldManager) log() logSDK.Logger {
	if m != nil && m.logger != nil {
		return m.logger
	}
	return log.Logger.Named("hold_manager")
}
