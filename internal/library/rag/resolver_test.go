package rag

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	ragsvc "github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
)

type stubKeyInfoService struct {
	input    ragsvc.ExtractInput
	calls    int
	contexts []string
	err      error
}

func (s *stubKeyInfoService) ExtractKeyInfo(_ context.Context, input ragsvc.ExtractInput) ([]string, error) {
	s.calls++
	s.input = input
	if s.err != nil {
		return nil, s.err
	}

	return s.contexts, nil
}

func testSettings() ragsvc.Settings {
	return ragsvc.Settings{
		Enabled:          true,
		TopKDefault:      3,
		TopKLimit:        10,
		MaxMaterialsSize: 64,
		MaxChunkChars:    500,
		SemanticWeight:   0.65,
		LexicalWeight:    0.35,
	}
}

// testCtx builds a gin context carrying the given Authorization header, which
// the resolver reads exactly like a real GraphQL request would.
func testCtx(authorization string) context.Context {
	gin.SetMode(gin.TestMode)
	gctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	gctx.Request = httptest.NewRequest(http.MethodPost, "/graphql", nil)
	if authorization != "" {
		gctx.Request.Header.Set("Authorization", authorization)
	}

	return gctx
}

func newTestResolver(svc KeyInfoService) *MutationResolver {
	return NewMutationResolver(svc, testSettings(), nil,
		WithBillingChecker(func(context.Context, string, oneapi.Price, string) error { return nil }))
}

func TestMutationResolver_ExtractKeyInfoSuccess(t *testing.T) {
	svc := &stubKeyInfoService{contexts: []string{"first", "second"}}
	r := newTestResolver(svc)

	got, err := r.ExtractKeyInfo(testCtx("Bearer sk-test"), "  who is it?  ", "  some materials  ", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, got.Contexts)
	require.Equal(t, "who is it?", got.Query)
	require.False(t, got.CreatedAt.GetTime().IsZero())

	require.Equal(t, 1, svc.calls)
	require.Equal(t, "who is it?", svc.input.Query)
	require.Equal(t, "some materials", svc.input.Materials)
	require.Equal(t, 3, svc.input.TopK, "top_k falls back to the configured default")
	require.Equal(t, "sk-test", svc.input.APIKey)
	require.NotEmpty(t, svc.input.TaskID)

	authCtx, err := mcpauth.DeriveFromAPIKey("sk-test")
	require.NoError(t, err)
	require.Equal(t, authCtx.UserID, svc.input.UserID, "tenant isolation matches the MCP tool")
}

func TestMutationResolver_ExtractKeyInfoGeneratesUniqueTaskID(t *testing.T) {
	svc := &stubKeyInfoService{contexts: []string{"ctx"}}
	r := newTestResolver(svc)

	_, err := r.ExtractKeyInfo(testCtx("Bearer sk-test"), "q", "materials", nil)
	require.NoError(t, err)
	first := svc.input.TaskID

	_, err = r.ExtractKeyInfo(testCtx("Bearer sk-test"), "q", "materials", nil)
	require.NoError(t, err)
	require.NotEqual(t, first, svc.input.TaskID)
}

func TestMutationResolver_ExtractKeyInfoHonoursTopK(t *testing.T) {
	svc := &stubKeyInfoService{contexts: []string{"ctx"}}
	r := newTestResolver(svc)

	topK := 7
	_, err := r.ExtractKeyInfo(testCtx("Bearer sk-test"), "q", "materials", &topK)
	require.NoError(t, err)
	require.Equal(t, 7, svc.input.TopK)
}

func TestMutationResolver_ExtractKeyInfoNilContexts(t *testing.T) {
	svc := &stubKeyInfoService{}
	r := newTestResolver(svc)

	got, err := r.ExtractKeyInfo(testCtx("Bearer sk-test"), "q", "materials", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Contexts, "a nil slice would break the non-null [String!]! contract")
	require.Empty(t, got.Contexts)
}

func TestMutationResolver_ExtractKeyInfoValidation(t *testing.T) {
	tooLarge := strings.Repeat("a", testSettings().MaxMaterialsSize+1)
	zero := 0
	tooBig := testSettings().TopKLimit + 1

	for _, tc := range []struct {
		name      string
		query     string
		materials string
		topK      *int
		wantErr   string
	}{
		{name: "empty query", query: "   ", materials: "m", wantErr: "query cannot be empty"},
		{name: "empty materials", query: "q", materials: "  ", wantErr: "materials cannot be empty"},
		{name: "materials too large", query: "q", materials: tooLarge, wantErr: "materials exceed maximum size"},
		{name: "top_k zero", query: "q", materials: "m", topK: &zero, wantErr: "top_k must be between 1 and 10"},
		{name: "top_k above limit", query: "q", materials: "m", topK: &tooBig, wantErr: "top_k must be between 1 and 10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubKeyInfoService{contexts: []string{"ctx"}}
			r := newTestResolver(svc)

			_, err := r.ExtractKeyInfo(testCtx("Bearer sk-test"), tc.query, tc.materials, tc.topK)
			require.ErrorContains(t, err, tc.wantErr)
			require.Zero(t, svc.calls, "invalid requests must not reach the service")
		})
	}
}

func TestMutationResolver_ExtractKeyInfoRequiresAuthorization(t *testing.T) {
	svc := &stubKeyInfoService{contexts: []string{"ctx"}}
	r := newTestResolver(svc)

	_, err := r.ExtractKeyInfo(testCtx(""), "q", "materials", nil)
	require.ErrorIs(t, err, mcpauth.ErrMissingAuthorization)
	require.Zero(t, svc.calls)
}

func TestMutationResolver_ExtractKeyInfoBillingDenied(t *testing.T) {
	svc := &stubKeyInfoService{contexts: []string{"ctx"}}
	r := NewMutationResolver(svc, testSettings(), nil,
		WithBillingChecker(func(context.Context, string, oneapi.Price, string) error {
			return errors.New("insufficient quota")
		}))

	_, err := r.ExtractKeyInfo(testCtx("Bearer sk-test"), "q", "materials", nil)
	require.ErrorContains(t, err, "insufficient quota")
	require.Zero(t, svc.calls, "billing is charged before any embedding work")
}

func TestMutationResolver_ExtractKeyInfoServiceError(t *testing.T) {
	svc := &stubKeyInfoService{err: errors.New("db down")}
	r := newTestResolver(svc)

	_, err := r.ExtractKeyInfo(testCtx("Bearer sk-test"), "q", "materials", nil)
	require.ErrorContains(t, err, "db down")
}

func TestMutationResolver_ExtractKeyInfoServiceUnavailable(t *testing.T) {
	r := newTestResolver(nil)

	_, err := r.ExtractKeyInfo(testCtx("Bearer sk-test"), "q", "materials", nil)
	require.ErrorContains(t, err, "rag service is not configured")
}
