package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/stretchr/testify/require"
)

// TestSPARootBoundary rejects symlink escapes without breaking ordinary assets.
func TestSPARootBoundary(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("outside-private-data"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "safe.txt"), []byte("public asset"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape.txt")))
	require.NoError(t, os.Symlink("safe.txt", filepath.Join(root, "alias.txt")))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "index.md")))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".well-known"), 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, ".well-known", "mcp.json")))
	h := &spaHandler{root: root, index: []byte("SPA index"), logger: glog.Shared}
	for _, path := range []string{"/escape.txt", "/.well-known/mcp", "/?mode=agent"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			require.NotContains(t, w.Body.String(), "outside-private-data")
			if path != "/?mode=agent" {
				require.Equal(t, http.StatusNotFound, w.Code)
			}
		})
	}
	for _, path := range []string{"/safe.txt", "/alias.txt"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, "public asset", w.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/safe.txt", nil)
	request.Header.Set("Range", "bytes=0-5")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request)
	require.Equal(t, http.StatusPartialContent, w.Code)
	require.Equal(t, "public", w.Body.String())
}
