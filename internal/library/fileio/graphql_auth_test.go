package fileio

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
)

// TestGraphQLSharedToolRejectsUnissuedBearer reproduces issue #86 on the
// GraphQL dual-published fields: the raw gin Authorization header must be
// validated before a tenant reaches persistence.
func TestGraphQLSharedToolRejectsUnissuedBearer(t *testing.T) {
	var authorityCalls atomic.Int64
	authtest.Swap(t, authtest.NewIssuedOnly(&authorityCalls))
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/query/", nil)
	ginCtx.Request.Header.Set("Authorization", "Bearer sk-random-graphql-tenant")

	spy := &afterTurnObserver{}
	resolver := &MutationResolver{NewResolver(nil, nil, spy, nil)}
	ack, err := resolver.MemoryAfterTurn(ginCtx, models.MemoryAfterTurnInput{})
	require.Error(t, err)
	require.Nil(t, ack)
	require.Zero(t, spy.calls, "an unvalidated GraphQL bearer must not reach persistence")
	require.Equal(t, int64(1), authorityCalls.Load(), "the GraphQL path must consult the credential authority")
}
