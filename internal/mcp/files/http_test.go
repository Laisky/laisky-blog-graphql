package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// httpAPIKey is the API key used by HTTP tests.
const httpAPIKey = "sk-files-http-1234567890abcdef"

// httpAPIKeyHash returns the SHA-256 hash of httpAPIKey.
func httpAPIKeyHash() string {
	sum := sha256.Sum256([]byte(httpAPIKey))
	return hex.EncodeToString(sum[:])
}

// newHTTPTestEnv constructs a service plus its HTTP handler for tests.
func newHTTPTestEnv(t *testing.T) (*Service, http.Handler, AuthContext) {
	t.Helper()
	svc := newVersionsTestService(t)
	handler := NewHTTPHandler(svc, log.Logger.Named("files_http_test"))
	auth := AuthContext{
		APIKey:     httpAPIKey,
		APIKeyHash: httpAPIKeyHash(),
	}
	return svc, handler, auth
}

// httpAuthHeader returns the Bearer header value for httpAPIKey.
func httpAuthHeader() string {
	return "Bearer " + httpAPIKey
}

// TestHTTP_ListVersions_OK exercises the GET /api/versions happy path.
func TestHTTP_ListVersions_OK(t *testing.T) {
	svc, handler, auth := newHTTPTestEnv(t)

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "A", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "B", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/versions?project=proj&path=/a.txt", nil)
	req.Header.Set("Authorization", httpAuthHeader())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	versions, ok := resp["versions"].([]any)
	require.True(t, ok)
	require.Len(t, versions, 1)
	first, ok := versions[0].(map[string]any)
	require.True(t, ok)
	require.NotNil(t, first["id"])
	require.NotNil(t, first["size"])
	require.NotNil(t, first["created_at"])
}

// TestHTTP_ListVersions_MissingAuth verifies that absent Authorization yields 401.
func TestHTTP_ListVersions_MissingAuth(t *testing.T) {
	_, handler, _ := newHTTPTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/versions?project=proj&path=/a.txt", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestHTTP_ListVersions_PathRequired verifies an empty path returns 400.
func TestHTTP_ListVersions_PathRequired(t *testing.T) {
	_, handler, _ := newHTTPTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/versions?project=proj", nil)
	req.Header.Set("Authorization", httpAuthHeader())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestHTTP_ReadVersion_UTF8 verifies UTF-8 round-trip via the read endpoint.
func TestHTTP_ReadVersion_UTF8(t *testing.T) {
	svc, handler, auth := newHTTPTestEnv(t)

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "hello", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "world", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	versionID := versions[0].ID

	url := fmt.Sprintf("/api/versions/%d/content?project=proj&path=/a.txt", versionID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", httpAuthHeader())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "utf-8", resp["content_encoding"])
	require.Equal(t, "hello", resp["content"])
}

// TestHTTP_ReadVersion_Base64 verifies non-UTF8 content encodes as base64.
func TestHTTP_ReadVersion_Base64(t *testing.T) {
	svc, handler, auth := newHTTPTestEnv(t)

	rawBytes := []byte{0xFF, 0xFE, 0x00, 0x01}
	_, err := svc.Write(context.Background(), auth, "proj", "/a.bin", string(rawBytes), "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/a.bin", "after", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.bin")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	versionID := versions[0].ID

	url := fmt.Sprintf("/api/versions/%d/content?project=proj&path=/a.bin", versionID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", httpAuthHeader())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "base64", resp["content_encoding"])
	encoded, ok := resp["content"].(string)
	require.True(t, ok)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	require.Equal(t, rawBytes, decoded)
}

// TestHTTP_ReadVersion_NotFound verifies a missing version yields 404.
func TestHTTP_ReadVersion_NotFound(t *testing.T) {
	_, handler, _ := newHTTPTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/versions/999999/content?project=proj&path=/a.txt", nil)
	req.Header.Set("Authorization", httpAuthHeader())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
}

// TestHTTP_RestoreVersion_RoundTrip verifies POST restore writes prior content back.
func TestHTTP_RestoreVersion_RoundTrip(t *testing.T) {
	svc, handler, auth := newHTTPTestEnv(t)

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "AAA", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "BBB", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	versionID := versions[0].ID

	url := fmt.Sprintf("/api/versions/%d/restore", versionID)
	body := bytes.NewBufferString(`{"project": "proj", "path": "/a.txt"}`)
	req := httptest.NewRequest(http.MethodPost, url, body)
	req.Header.Set("Authorization", httpAuthHeader())
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	read, err := svc.Read(context.Background(), auth, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "AAA", read.Content)
}

// TestHTTP_RestoreVersion_BadJSON verifies malformed payloads return 400.
func TestHTTP_RestoreVersion_BadJSON(t *testing.T) {
	_, handler, _ := newHTTPTestEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/api/versions/1/restore", bytes.NewBufferString(`{bogus`))
	req.Header.Set("Authorization", httpAuthHeader())
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestHTTP_PutFile_RoundTrip verifies PUT /api/file persists content and snapshots prior bytes.
func TestHTTP_PutFile_RoundTrip(t *testing.T) {
	svc, handler, auth := newHTTPTestEnv(t)

	body := bytes.NewBufferString(`{"project": "proj", "path": "/a.txt", "content": "first"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/file", body)
	req.Header.Set("Authorization", httpAuthHeader())
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	read, err := svc.Read(context.Background(), auth, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "first", read.Content)

	body = bytes.NewBufferString(`{"project": "proj", "path": "/a.txt", "content": "second"}`)
	req = httptest.NewRequest(http.MethodPut, "/api/file", body)
	req.Header.Set("Authorization", httpAuthHeader())
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	read, err = svc.Read(context.Background(), auth, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "second", read.Content)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	v, err := svc.ReadVersion(context.Background(), auth, "proj", "/a.txt", versions[0].ID)
	require.NoError(t, err)
	require.Equal(t, []byte("first"), v.Content)
}

// TestHTTP_PutFile_OversizedBody verifies oversized payloads are rejected.
func TestHTTP_PutFile_OversizedBody(t *testing.T) {
	_, handler, _ := newHTTPTestEnv(t)

	// MaxPayloadBytes from versionsTestSettings is the LoadSettingsFromConfig default
	// (2_000_000). Build content larger than that limit but smaller than the 32MB read cap.
	huge := strings.Repeat("a", 3_000_000)
	payload := map[string]any{
		"project": "proj",
		"path":    "/a.txt",
		"content": huge,
	}
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut, "/api/file", bytes.NewReader(encoded))
	req.Header.Set("Authorization", httpAuthHeader())
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

// TestHTTP_UnknownRoute verifies unknown paths return 404.
func TestHTTP_UnknownRoute(t *testing.T) {
	_, handler, _ := newHTTPTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/garbage", nil)
	req.Header.Set("Authorization", httpAuthHeader())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
}
