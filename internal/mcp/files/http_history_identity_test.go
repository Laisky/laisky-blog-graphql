package files

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHTTPHistoryIDsRoundTripWithoutJSONNumberRounding keeps adjacent IDs above
// JavaScript's safe-integer range distinct through list, content and restore.
func TestHTTPHistoryIDsRoundTripWithoutJSONNumberRounding(t *testing.T) {
	svc, handler, auth := newHTTPTestEnv(t)
	ctx := context.Background()
	for _, content := range []string{"A", "B", "C"} {
		_, err := svc.Write(ctx, auth, "proj", "/a.txt", content, "utf-8", 0, WriteModeTruncate)
		require.NoError(t, err)
	}
	versions, err := svc.ListVersions(ctx, auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, 2)
	const low, high int64 = 9007199254740992, 9007199254740993
	for i, target := range []int64{high, low} {
		_, err := svc.db.ExecContext(ctx, `UPDATE mcp_file_versions SET id=?
			WHERE id=? AND apikey_hash=? AND project=? AND path=? AND system_owner=?`,
			target, versions[i].ID, auth.APIKeyHash, "proj", "/a.txt", "")
		require.NoError(t, err)
	}
	request := func(method, url, body, match string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Authorization", httpAuthHeader())
		if match != "" {
			req.Header.Set("If-Match", `"`+match+`"`)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	listed := request(http.MethodGet, "/api/versions?project=proj&path=/a.txt", "", "")
	require.Equal(t, http.StatusOK, listed.Code)
	var result struct {
		Versions []struct {
			ID string `json:"id"`
		} `json:"versions"`
	}
	require.NoError(t, json.Unmarshal(listed.Body.Bytes(), &result))
	require.Equal(t, "9007199254740993", result.Versions[0].ID)
	require.Equal(t, "9007199254740992", result.Versions[1].ID)
	selected := result.Versions[0].ID
	preview := request(http.MethodGet, "/api/versions/"+selected+"/content?project=proj&path=/a.txt", "", "")
	require.Equal(t, http.StatusOK, preview.Code)
	var content struct {
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(preview.Body.Bytes(), &content))
	require.Equal(t, "B", content.Content)
	live, err := svc.Read(ctx, auth, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	restored := request(http.MethodPost, "/api/versions/"+selected+"/restore", `{"project":"proj","path":"/a.txt"}`, live.Version)
	require.Equal(t, http.StatusOK, restored.Code, restored.Body.String())
	live, err = svc.Read(ctx, auth, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "B", live.Content)
}
