package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/general/service"
	"github.com/Laisky/laisky-blog-graphql/library/auth"
)

// TestGeneralAddLLMStormTaskRetired verifies that retirement never needs auth,
// a task store, or a worker and never exposes a caller's supplied credentials.
func TestGeneralAddLLMStormTaskRetired(t *testing.T) {
	originalService, originalAuth := service.Instance, auth.Instance
	t.Cleanup(func() {
		service.Instance, auth.Instance = originalService, originalAuth
	})
	auth.Instance = nil

	for _, initialized := range []bool{false, true} {
		name := "uninitialized"
		if initialized {
			name = "initialized_without_task_store"
		}
		t.Run(name, func(t *testing.T) {
			service.Instance = nil
			if initialized {
				service.Instance = &service.Type{}
			}
			for _, input := range []struct {
				name, prompt, apiKey string
			}{
				{name: "ordinary", prompt: "research a topic", apiKey: "synthetic-private-key"},
				{name: "empty"},
			} {
				t.Run(input.name, func(t *testing.T) {
					contexts := []context.Context{context.Background()}
					canceled, cancel := context.WithCancel(context.Background())
					cancel()
					contexts = append(contexts, canceled)
					for _, ctx := range contexts {
						var taskID string
						var err error
						require.NotPanics(t, func() {
							taskID, err = (&MutationResolver{}).GeneralAddLLMStormTask(ctx, input.prompt, input.apiKey)
						})
						require.Empty(t, taskID)
						require.EqualError(t, err, "llm-storm research has been retired; new tasks are unavailable")
					}
				})
			}
		})
	}
}
