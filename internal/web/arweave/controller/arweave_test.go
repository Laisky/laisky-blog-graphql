package controller

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
	"github.com/Laisky/laisky-blog-graphql/library/auth"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
)

// fakeUploadStore stands in for the telegram upload DAO. Its API-key upload
// charges through the real one-api consume call, like the DAO, so the test
// observes every consume that reaches one-api.
type fakeUploadStore struct {
	apikeyUploads atomic.Int64
	jwtUploads    atomic.Int64
	lastKey       atomic.Value
	lastType      atomic.Value
}

// UploadFile records an upload without billing.
func (f *fakeUploadStore) UploadFile(_ context.Context, _ []byte, contentType string) (string, error) {
	f.jwtUploads.Add(1)
	f.lastType.Store(contentType)
	return "tx-jwt", nil
}

// UploadFileWithApikey charges apikey through one-api, then records the upload.
func (f *fakeUploadStore) UploadFileWithApikey(ctx context.Context, apikey string, _ []byte, contentType string) (string, error) {
	f.apikeyUploads.Add(1)
	f.lastKey.Store(apikey)
	f.lastType.Store(contentType)
	if err := oneapi.CheckUserExternalBilling(ctx, apikey, oneapi.Price(1), "upload file"); err != nil {
		return "", err
	}
	return "tx-apikey", nil
}

// arweaveFixture points one-api consumes at a counting server and returns a
// resolver over a fake upload store.
func arweaveFixture(t *testing.T) (*MutationResolver, *fakeUploadStore, *atomic.Int64) {
	t.Helper()
	var consumes atomic.Int64
	billing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/token/consume" {
			consumes.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	t.Cleanup(billing.Close)
	previousBase := oneapi.BillingAPI
	oneapi.BillingAPI = billing.URL
	t.Cleanup(func() { oneapi.BillingAPI = previousBase })

	previousAuth := auth.Instance
	require.NoError(t, auth.Initialize([]byte(strings.Repeat("s", 32))))
	t.Cleanup(func() { auth.Instance = previousAuth })

	store := &fakeUploadStore{}
	return &MutationResolver{uploadDao: store}, store, &consumes
}

// ginContext returns a GraphQL request context carrying authorization.
func ginContext(authorization string) *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/query/", nil)
	ctx.Request.RemoteAddr = "203.0.113.40:5555"
	if authorization != "" {
		ctx.Request.Header.Set("Authorization", authorization)
	}
	return ctx
}

// TestArweaveUploadAuthenticatesBearerBeforeCharging reproduces the ungated
// API-key path: a bearer that is not a blog JWT went straight to one-api's
// consume, so a junk-bearer flood became ungated one-api calls. It now goes
// through the shared authenticator first.
func TestArweaveUploadAuthenticatesBearerBeforeCharging(t *testing.T) {
	resolver, store, consumes := arweaveFixture(t)
	var authorityCalls atomic.Int64
	authtest.Swap(t, authtest.NewIssuedOnly(&authorityCalls, "sk-arweave-valid"))
	contentType := "text/plain"
	payload := base64.StdEncoding.EncodeToString([]byte("hello"))

	for range 3 {
		response, err := resolver.ArweaveUpload(ginContext("Bearer sk-junk"), payload, &contentType)
		require.ErrorIs(t, err, mcpauth.ErrCredentialRejected)
		require.Nil(t, response)
	}
	require.Zero(t, store.apikeyUploads.Load(), "a rejected bearer never uploads")
	require.Zero(t, consumes.Load(), "a rejected bearer never reaches one-api's consume")
	require.Equal(t, int64(1), authorityCalls.Load(), "repeated junk is answered from the negative cache")

	_, err := resolver.ArweaveUpload(ginContext("Bearer sk-"+strings.Repeat("k", mcpauth.MaxAPIKeyLength)), payload, &contentType)
	require.ErrorIs(t, err, mcpauth.ErrInvalidAuthorization)
	require.Equal(t, int64(1), authorityCalls.Load(), "an oversized bearer never reaches the authority")

	response, err := resolver.ArweaveUpload(ginContext("Bearer workspace@sk-arweave-valid"), payload, &contentType)
	require.NoError(t, err)
	require.Equal(t, "tx-apikey", response.FileID)
	require.Equal(t, "sk-arweave-valid", store.lastKey.Load(), "the verified key, not the raw header, is charged")
	require.Equal(t, int64(1), consumes.Load())
}

// TestArweaveUploadUnavailableValidationIsRetryable: without a verdict the
// upload fails closed and nothing is charged.
func TestArweaveUploadUnavailableValidationIsRetryable(t *testing.T) {
	resolver, store, consumes := arweaveFixture(t)
	authtest.Swap(t, nil)
	contentType := "text/plain"

	_, err := resolver.ArweaveUpload(ginContext("Bearer sk-anything"), base64.StdEncoding.EncodeToString([]byte("x")), &contentType)
	require.True(t, mcpauth.IsUnavailable(err), "%v", err)
	require.Zero(t, store.apikeyUploads.Load())
	require.Zero(t, consumes.Load())
}

// TestArweaveUploadToleratesNilContentType: an explicit null contentType used
// to dereference a nil pointer; it now falls back to the schema default.
func TestArweaveUploadToleratesNilContentType(t *testing.T) {
	resolver, store, _ := arweaveFixture(t)
	authtest.Swap(t, authtest.NewIssuedOnly(nil, "sk-arweave-valid"))

	response, err := resolver.ArweaveUpload(ginContext("Bearer sk-arweave-valid"), base64.StdEncoding.EncodeToString([]byte("x")), nil)
	require.NoError(t, err)
	require.Equal(t, "tx-apikey", response.FileID)
	require.Equal(t, defaultArweaveContentType, store.lastType.Load())
}
