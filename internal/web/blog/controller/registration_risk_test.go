package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/library/clientip"
)

// TestRegistrationGateCountsCodeFailuresPerEmail is the database-free form of
// TestMongoRegistrationCodeAccountBudgetSurvivesIPRotation: registration
// attempts admitted from rotating addresses and settled as failed must
// escalate the email they name, including for a fresh code request, and
// concurrent admissions must reserve the email's budget.
func TestRegistrationGateCountsCodeFailuresPerEmail(t *testing.T) {
	setTurnstileEnabledSite(t)
	tracker := newAuthChallengeTracker(time.Minute, 100, 15*time.Minute, 3, 100)
	withAuthChallengeTracker(t, tracker)

	var held []*authAttempt
	for i := range 3 {
		attempt, err := admitAuthRequest(clientip.WithIP(context.Background(), fmt.Sprintf("203.0.113.%d", i+1)), nil,
			registerFlow("newcomer@example.test"))
		require.NoError(t, err, "admission %d is within the budget", i+1)
		held = append(held, attempt)
	}
	_, err := admitAuthRequest(clientip.WithIP(context.Background(), "203.0.113.50"), nil, registerFlow("Newcomer@Example.test"))
	require.ErrorIs(t, err, model.ErrTurnstileRequired, "in-flight registrations reserve the email's budget")

	for _, attempt := range held {
		attempt.failed("")
	}
	_, err = admitAuthRequest(clientip.WithIP(context.Background(), "203.0.113.60"), nil, registerFlow("newcomer@example.test"))
	require.ErrorIs(t, err, model.ErrTurnstileRequired, "recorded code failures escalate the email from any address")
	_, err = admitAuthRequest(clientip.WithIP(context.Background(), "203.0.113.61"), nil, loginFlow("newcomer@example.test"))
	require.ErrorIs(t, err, model.ErrTurnstileRequired, "registration and login share one budget per email")

	attempt, err := admitAuthRequest(clientip.WithIP(context.Background(), "203.0.113.62"), nil, registerFlow("other@example.test"))
	require.NoError(t, err, "other emails are not challenged")
	attempt.finish()
}
