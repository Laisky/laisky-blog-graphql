package fileio

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
)

// afterTurnObserver deliberately accepts every request: only the real resolver
// can reject a negative boundary before it reaches this persistence spy.
type afterTurnObserver struct {
	MemoryService
	calls   int
	request mcpmemory.AfterTurnRequest
}

// AfterTurn records the request without applying the validation under test.
func (s *afterTurnObserver) AfterTurn(_ context.Context, _ files.AuthContext, request mcpmemory.AfterTurnRequest) error {
	s.calls++
	s.request = request
	return nil
}

// reviewMemoryContext supplies a synthetic authenticated caller without a
// network request or any production credentials.
func reviewMemoryContext() context.Context {
	return mcpauth.WithContext(context.Background(), &mcpauth.Context{
		APIKey: "synthetic-review-key", APIKeyHash: "synthetic-review-hash",
		UserID: "review-user", UserIdentity: "review-user",
	})
}

// TestMemoryAfterTurnRejectsNegativeBoundaries exercises the stable public
// resolver method, so the same test fails before the converter signature fix.
func TestMemoryAfterTurnRejectsNegativeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name         string
		start, count int
		field        string
	}{
		{"negative start", -1, 1, "current_input_start"},
		{"negative count", 0, -1, "current_input_count"},
		{"both negative", -1, -1, "current_input_start"},
		{"minimum GraphQL start", -2147483648, 1, "current_input_start"},
		{"minimum GraphQL count", 0, -2147483648, "current_input_count"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &afterTurnObserver{}
			resolver := &MutationResolver{NewResolver(nil, nil, spy, nil)}
			input := models.MemoryAfterTurnInput{CurrentInputStart: &tc.start, CurrentInputCount: &tc.count}
			before, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			ack, err := resolver.MemoryAfterTurn(reviewMemoryContext(), input)
			if err == nil || !strings.Contains(err.Error(), tc.field+" cannot be negative") || ack != nil {
				t.Errorf("negative boundary was not rejected: ack=%+v error=%v", ack, err)
			}
			if spy.calls != 0 {
				t.Errorf("invalid request reached persistence %d times with boundaries %d/%d",
					spy.calls, spy.request.CurrentInputStart, spy.request.CurrentInputCount)
			}
			after, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Error("validation mutated caller-owned input")
			}
		})
	}
}

// TestMemoryAfterTurnPreservesValidBoundaries ensures nil/zero defaults and
// positive indexes survive conversion and dispatch exactly once.
func TestMemoryAfterTurnPreservesValidBoundaries(t *testing.T) {
	zero, one, two := 0, 1, 2
	for _, tc := range []struct {
		name                 string
		start, count         *int
		wantStart, wantCount int
	}{
		{"omitted", nil, nil, 0, 0},
		{"explicit zero", &zero, &zero, 0, 0},
		{"positive", &one, &one, 1, 1},
		{"mixed zero and positive", &zero, &two, 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &afterTurnObserver{}
			resolver := &MutationResolver{NewResolver(nil, nil, spy, nil)}
			project, session, turn, role, text := " project ", " session ", " turn ", "user", "synthetic content"
			item := &models.MemoryResponseItemInput{Type: "message", Role: &role,
				Content: []*models.MemoryContentPartInput{{Type: "input_text", Text: &text}}}
			input := models.MemoryAfterTurnInput{Project: &project, SessionID: &session, TurnID: &turn,
				CurrentInputStart: tc.start, CurrentInputCount: tc.count,
				ConversationItems: []*models.MemoryResponseItemInput{item, item},
				InputItems:        []*models.MemoryResponseItemInput{item}, OutputItems: []*models.MemoryResponseItemInput{item}}
			ack, err := resolver.MemoryAfterTurn(reviewMemoryContext(), input)
			if err != nil || ack == nil || !ack.Ok || spy.calls != 1 {
				t.Fatalf("valid request failed: ack=%+v error=%v calls=%d", ack, err, spy.calls)
			}
			got := spy.request
			if got.CurrentInputStart != tc.wantStart || got.CurrentInputCount != tc.wantCount {
				t.Fatalf("boundaries changed: got %d/%d want %d/%d", got.CurrentInputStart, got.CurrentInputCount, tc.wantStart, tc.wantCount)
			}
			if got.Project != "project" || got.SessionID != "session" || got.TurnID != "turn" {
				t.Errorf("routing defaults/normalization changed: %+v", got)
			}
			if !reflect.DeepEqual(got.InputItems, requestItems(input.InputItems)) ||
				!reflect.DeepEqual(got.OutputItems, requestItems(input.OutputItems)) ||
				!reflect.DeepEqual(got.ConversationItems, requestItems(input.ConversationItems)) {
				t.Error("conversion changed the conversation contents")
			}
		})
	}
}
