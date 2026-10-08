package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	general "github.com/Laisky/laisky-blog-graphql/internal/web/general/controller"
	"github.com/Laisky/laisky-blog-graphql/internal/web/general/service"
	"github.com/Laisky/laisky-blog-graphql/library/auth"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

type stormGraphQLResponse struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// TestLLMStormGraphQLRetirement verifies retirement against the executable schema
// and an authenticated, functioning task store rather than dependency failures.
func TestLLMStormGraphQLRetirement(t *testing.T) {
	originalService, originalAuth := service.Instance, auth.Instance
	t.Cleanup(func() {
		service.Instance, auth.Instance = originalService, originalAuth
	})
	store := miniredis.RunT(t)
	db := rlibs.NewDB(&redis.Options{Addr: store.Addr()})
	t.Cleanup(func() { require.NoError(t, db.GetDB().Close()) })
	service.Instance = service.NewService(nil)
	service.Instance.SetTasksDB(db)
	secret := []byte("synthetic-llmstorm-retirement-test-secret")
	require.NoError(t, auth.Initialize(secret))
	token, err := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, jwtlib.RegisteredClaims{
		Subject:   "synthetic-worker",
		ExpiresAt: jwtlib.NewNumericDate(time.Now().Add(time.Hour)),
	}).SignedString(secret)
	require.NoError(t, err)

	// Keep pre-existing work and historical results intact throughout retirement.
	store.Push(rlibs.KeyTaskLLMStormPending, "preexisting-research-task")
	const resultJSON = `{"task_id":"historical-task","created_at":"2025-01-01T12:34:56Z","status":"success","finished_at":"2025-01-01T12:35:56Z","prompt":"historical prompt","api_key":"synthetic-historical-key","result_article":"historical article","runner":"synthetic-worker","result_references":{"url_to_unified_index":{"https://example.com":1},"url_to_info":{}}}`
	require.NoError(t, store.Set(rlibs.KeyPrefixTaskLLMStormResult+"historical-task", resultJSON))

	// Wire the general subgraph exactly as the production resolver does, while
	// avoiding initialization of unrelated external-service controllers.
	resolver := &Resolver{
		queryResolver:    &queryResolver{genaralQuery: genaralQuery{QueryResolver: general.QueryResolver{}}},
		mutationResolver: &mutationResolver{generalMutation: generalMutation{MutationResolver: &general.MutationResolver{}}},
	}
	server := handler.NewDefaultServer(NewExecutableSchema(Config{Resolvers: resolver}))
	execute := func(t *testing.T, query string, authenticated bool) stormGraphQLResponse {
		t.Helper()
		body, marshalErr := json.Marshal(map[string]string{"query": query})
		require.NoError(t, marshalErr)
		req := httptest.NewRequest(http.MethodPost, "/query", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		if authenticated {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		recorder := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(recorder)
		ginCtx.Request = req
		req = req.WithContext(context.WithValue(req.Context(), gmw.CtxKeyGin, ginCtx))
		server.ServeHTTP(recorder, req)
		require.Equal(t, http.StatusOK, recorder.Code)
		var response stormGraphQLResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		return response
	}

	t.Run("new research is rejected without changing pending work", func(t *testing.T) {
		response := execute(t, `mutation { GeneralAddLLMStormTask(prompt:"synthetic research",api_key:"synthetic-new-key") }`, true)
		pending, listErr := store.List(rlibs.KeyTaskLLMStormPending)
		require.NoError(t, listErr)
		// This fails red with two entries when the old real enqueue path succeeds.
		require.Equal(t, []string{"preexisting-research-task"}, pending)
		require.Len(t, response.Errors, 1)
		require.Equal(t, "llm-storm research has been retired; new tasks are unavailable", response.Errors[0].Message)
		require.Empty(t, response.Data)
	})

	t.Run("historical results remain readable and unchanged", func(t *testing.T) {
		response := execute(t, `query { GeneralGetLLMStormTaskResult(task_id:"historical-task") { task_id status prompt result_article result_references created_at finished_at } }`, true)
		require.Empty(t, response.Errors)
		var result map[string]any
		require.NoError(t, json.Unmarshal(response.Data["GeneralGetLLMStormTaskResult"], &result))
		require.Equal(t, "historical-task", result["task_id"])
		require.Equal(t, "success", result["status"])
		require.Equal(t, "historical article", result["result_article"])
		require.Equal(t, "historical prompt", result["prompt"])
		require.Contains(t, result["result_references"], "url_to_unified_index")
		stored, getErr := store.Get(rlibs.KeyPrefixTaskLLMStormResult + "historical-task")
		require.NoError(t, getErr)
		require.Equal(t, resultJSON, stored)
	})

	t.Run("historical reads retain authentication", func(t *testing.T) {
		response := execute(t, `query { GeneralGetLLMStormTaskResult(task_id:"historical-task") { task_id } }`, false)
		require.Len(t, response.Errors, 1)
		require.Contains(t, response.Errors[0].Message, "validate worker")
		require.Empty(t, response.Data)
	})

	t.Run("ordinary crawler enqueue and dequeue still work", func(t *testing.T) {
		response := execute(t, `mutation { GeneralAddHTMLCrawlerTask(url:"https://example.com") }`, true)
		require.Empty(t, response.Errors)
		var taskID string
		require.NoError(t, json.Unmarshal(response.Data["GeneralAddHTMLCrawlerTask"], &taskID))
		require.NotEmpty(t, taskID)
		pending, listErr := store.List(rlibs.KeyTaskHTMLCrawlerPending)
		require.NoError(t, listErr)
		require.Len(t, pending, 1)
		response = execute(t, `query { GeneralGetHTMLCrawlerTask { task_id url status } }`, true)
		require.Empty(t, response.Errors)
		var task map[string]any
		require.NoError(t, json.Unmarshal(response.Data["GeneralGetHTMLCrawlerTask"], &task))
		require.Equal(t, taskID, task["task_id"])
		require.Equal(t, "https://example.com", task["url"])
		require.Equal(t, rlibs.TaskStatusPending, task["status"])
		require.False(t, store.Exists(rlibs.KeyTaskHTMLCrawlerPending))
	})
}
