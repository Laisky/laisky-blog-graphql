package service

import (
	"time"

	gutils "github.com/Laisky/go-utils/v6"
	"github.com/google/uuid"
)

const askUserSessionTTL = 15 * time.Minute

type askUserSession struct {
	RequestID   uuid.UUID
	PromptMsgID int
	ExpiresAt   time.Time
}

func (s *Telegram) trackAskUserSession(uid int64, promptMsgID int, reqID uuid.UUID) {
	if s.askUserSessions == nil {
		return
	}

	s.askUserSessions.Store(uid, &askUserSession{
		RequestID:   reqID,
		PromptMsgID: promptMsgID,
		ExpiresAt:   gutils.Clock.GetUTCNow().Add(askUserSessionTTL),
	})
}

func (s *Telegram) getAskUserSession(uid int64) (*askUserSession, bool) {
	if s.askUserSessions == nil {
		return nil, false
	}

	raw, ok := s.askUserSessions.Load(uid)
	if !ok {
		return nil, false
	}

	sess := raw.(*askUserSession)
	if gutils.Clock.GetUTCNow().After(sess.ExpiresAt) {
		s.askUserSessions.Delete(uid)
		return nil, false
	}

	return sess, true
}

func (s *Telegram) clearAskUserSession(uid int64, promptMsgID int, reqID uuid.UUID) {
	if s.askUserSessions == nil {
		if promptMsgID != 0 {
			s.askUserRequests.Delete(promptMsgID)
		}
		if promptMsgID == 0 && reqID != uuid.Nil {
			s.removeAskUserRequestByID(reqID)
		}
		return
	}

	if promptMsgID != 0 {
		s.askUserRequests.Delete(promptMsgID)
	} else if reqID != uuid.Nil {
		s.removeAskUserRequestByID(reqID)
	}

	if uid == 0 {
		return
	}

	raw, ok := s.askUserSessions.Load(uid)
	if !ok {
		return
	}

	sess := raw.(*askUserSession)
	switch {
	case promptMsgID != 0 && sess.PromptMsgID == promptMsgID:
		s.askUserSessions.Delete(uid)
	case reqID != uuid.Nil && sess.RequestID == reqID:
		s.askUserSessions.Delete(uid)
	}
}

func (s *Telegram) removeAskUserRequestByID(reqID uuid.UUID) {
	if reqID == uuid.Nil {
		return
	}

	s.askUserRequests.Range(func(key, value any) bool {
		stored, ok := value.(uuid.UUID)
		if !ok {
			return true
		}
		if stored == reqID {
			s.askUserRequests.Delete(key)
			return false
		}
		return true
	})
}
