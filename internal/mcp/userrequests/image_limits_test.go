package userrequests

import (
	"bytes"
	"context"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/imageproc"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/storage"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// TestMultipartLimitsAreAppliedBeforeAttachmentAllocation exercises length-independent body and per-image limits.
func TestMultipartLimitsAreAppliedBeforeAttachmentAllocation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  int
		count int
		want  error
	}{
		{"valid at boundary", 32, 2, nil},
		{"one oversized image", 33, 1, imageproc.ErrImageTooLarge},
		{"too many attachments", 1, 3, ErrTooManyImages},
		{"body exceeds total cap", (2 << 20) + 65, 1, imageproc.ErrImageTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			for i := 0; i < tc.count; i++ {
				w, err := mw.CreateFormFile("images", "test.bin")
				require.NoError(t, err)
				_, err = w.Write(bytes.Repeat([]byte{'x'}, tc.size))
				require.NoError(t, err)
			}
			require.NoError(t, mw.Close())
			req := httptest.NewRequest(http.MethodPost, "/", &buf)
			req.ContentLength = -1
			req.Header.Set("Content-Type", mw.FormDataContentType())
			settings := defaultImageSettings()
			settings.PerImageMaxBytes = 32
			settings.MaxPerRequest = 2
			h := &httpHandler{imageManager: NewImageManager(storage.NewFakeStore(), nil, settings), logger: log.Logger}
			_, _, attachments, err := h.parseMultipart(req)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
				require.Nil(t, attachments)
				return
			}
			require.NoError(t, err)
			require.Len(t, attachments, tc.count)
			require.Len(t, attachments[0].FileBytes, tc.size)
		})
	}
}

// TestMultipartRejectsOverflowAndCanceledRequests prevents configuration overflow or canceled requests starting reads.
func TestMultipartRejectsOverflowAndCanceledRequests(t *testing.T) {
	settings := defaultImageSettings()
	settings.PerImageMaxBytes = math.MaxInt64
	settings.MaxPerRequest = 2
	h := &httpHandler{imageManager: NewImageManager(storage.NewFakeStore(), nil, settings), logger: log.Logger}
	_, _, _, err := h.parseMultipart(httptest.NewRequest(http.MethodPost, "/", nil))
	require.Error(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	req.Body = io.NopCloser(panicRead{})
	require.ErrorIs(t, parseBoundedMultipart(req, 1024), context.Canceled)
}

type panicRead struct{}

func (panicRead) Read([]byte) (int, error) { panic("canceled request must not be read") }
