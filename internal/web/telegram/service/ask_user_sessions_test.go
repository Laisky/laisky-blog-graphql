package service

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAskUserSessionLifecycle(t *testing.T) {
	tel := &Telegram{
		askUserSessions: new(sync.Map),
		askUserRequests: new(sync.Map),
	}

	reqID := uuid.New()
	promptID := 101

	tel.trackAskUserSession(1, promptID, reqID)
	tel.askUserRequests.Store(promptID, reqID)

	sess, ok := tel.getAskUserSession(1)
	require.True(t, ok)
	require.Equal(t, reqID, sess.RequestID)

	tel.clearAskUserSession(1, promptID, reqID)
	_, ok = tel.getAskUserSession(1)
	require.False(t, ok)

	_, exists := tel.askUserRequests.Load(promptID)
	require.False(t, exists)
}

func TestAskUserSessionClearKeepsNewerSession(t *testing.T) {
	tel := &Telegram{
		askUserSessions: new(sync.Map),
		askUserRequests: new(sync.Map),
	}

	oldReq := uuid.New()
	newReq := uuid.New()

	tel.trackAskUserSession(2, 10, oldReq)
	tel.trackAskUserSession(2, 11, newReq)
	tel.askUserRequests.Store(10, oldReq)
	tel.askUserRequests.Store(11, newReq)

	tel.clearAskUserSession(2, 10, oldReq)

	sess, ok := tel.getAskUserSession(2)
	require.True(t, ok)
	require.Equal(t, newReq, sess.RequestID)

	_, existsOld := tel.askUserRequests.Load(10)
	require.False(t, existsOld)
	_, existsNew := tel.askUserRequests.Load(11)
	require.True(t, existsNew)
}

func TestClearAskUserSessionByRequestRemovesMapping(t *testing.T) {
	tel := &Telegram{
		askUserSessions: new(sync.Map),
		askUserRequests: new(sync.Map),
	}

	reqID := uuid.New()
	tel.trackAskUserSession(3, 21, reqID)
	tel.askUserRequests.Store(21, reqID)

	tel.clearAskUserSession(3, 0, reqID)

	_, ok := tel.getAskUserSession(3)
	require.False(t, ok)

	_, exists := tel.askUserRequests.Load(21)
	require.False(t, exists)
}

func TestGetAskUserSessionExpires(t *testing.T) {
	tel := &Telegram{
		askUserSessions: new(sync.Map),
		askUserRequests: new(sync.Map),
	}

	reqID := uuid.New()
	tel.askUserSessions.Store(4, &askUserSession{
		RequestID:   reqID,
		PromptMsgID: 33,
		ExpiresAt:   time.Now().Add(-time.Minute),
	})

	_, ok := tel.getAskUserSession(4)
	require.False(t, ok)
}
