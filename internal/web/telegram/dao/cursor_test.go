package dao

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// stalledCursor blocks Close until its context ends, as killCursors does on a
// stalled connection, and records the context it was given.
type stalledCursor struct {
	alive       bool
	hasDeadline bool
}

// Close records whether ctx was still live and bounded, then blocks until it ends.
func (c *stalledCursor) Close(ctx context.Context) error {
	c.alive = ctx.Err() == nil
	_, c.hasDeadline = ctx.Deadline()
	<-ctx.Done()
	return errors.WithStack(ctx.Err())
}

// TestCloseCursorIsBoundedAfterTheReadEnds pins the cleanup half of review
// finding 2 without MongoDB; TestMongoAlertCredentialCursorCleanupIsBounded
// covers it against a server. A read whose context already ended still kills its
// cursor, but that close returns within cursorCloseTimeout even when it stalls.
func TestCloseCursorIsBoundedAfterTheReadEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		readCtx, cancel := context.WithCancel(t.Context())
		cancel()
		cursor := new(stalledCursor)
		started := time.Now()
		err := closeCursor(readCtx, cursor)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Equal(t, cursorCloseTimeout, time.Since(started))
		require.True(t, cursor.alive, "an ended read still gets to kill its cursor")
		require.True(t, cursor.hasDeadline, "cleanup has a deadline of its own")
	})
}
