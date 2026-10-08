package askuser

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/stretchr/testify/require"
)

// TestHTTPStaticAssetRangeLimit exercises FileServer through the application's asset mux.
func TestHTTPStaticAssetRangeLimit(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("0123456789abcdef", 64)
	require.NoError(t, os.Mkdir(filepath.Join(root, "assets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>Ask user</html>"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "assets", "range.txt"), []byte(content), 0o600))
	t.Setenv("MCP_ASKUSER_DIST_DIR", root)
	// Exercise the patched default limit, independently of a developer's GODEBUG override.
	t.Setenv("GODEBUG", "")
	handler := NewHTTPHandler(nil, glog.Shared)
	require.NotNil(t, handler)

	single := httptest.NewRequest(http.MethodGet, "/assets/range.txt", nil)
	single.Header.Set("Range", "bytes=0-5")
	singleResponse := httptest.NewRecorder()
	handler.ServeHTTP(singleResponse, single)
	require.Equal(t, http.StatusPartialContent, singleResponse.Code)
	require.Equal(t, "012345", singleResponse.Body.String())
	require.Equal(t, "bytes 0-5/1024", singleResponse.Header().Get("Content-Range"))

	for _, count := range []int{200, 201} {
		t.Run(fmt.Sprintf("%d_ranges", count), func(t *testing.T) {
			ranges := make([]string, count)
			for i := range ranges {
				ranges[i] = fmt.Sprintf("%d-%d", i, i)
			}
			request := httptest.NewRequest(http.MethodGet, "/assets/range.txt", nil)
			request.Header.Set("Range", "bytes="+strings.Join(ranges, ","))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			// Distinct valid ranges total less than the file size, avoiding the older
			// sum-of-range-sizes defense as an accidental positive control.
			if count > 200 {
				require.Equal(t, http.StatusOK, response.Code)
				require.Equal(t, content, response.Body.String())
				require.Empty(t, response.Header().Get("Content-Range"))
				require.NotContains(t, response.Header().Get("Content-Type"), "multipart/byteranges")
				return
			}
			require.Equal(t, http.StatusPartialContent, response.Code)
			mediaType, params, err := mime.ParseMediaType(response.Header().Get("Content-Type"))
			require.NoError(t, err)
			require.Equal(t, "multipart/byteranges", mediaType)
			require.NotEmpty(t, params["boundary"])
			reader := multipart.NewReader(response.Body, params["boundary"])
			for i := range count {
				part, err := reader.NextPart()
				require.NoError(t, err)
				require.Equal(t, fmt.Sprintf("bytes %d-%d/1024", i, i), part.Header.Get("Content-Range"))
				body, err := io.ReadAll(part)
				require.NoError(t, err)
				require.Equal(t, content[i:i+1], string(body))
				require.NoError(t, part.Close())
			}
			_, err = reader.NextPart()
			require.ErrorIs(t, err, io.EOF)
		})
	}
}
