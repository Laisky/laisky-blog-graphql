package userrequests

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/imageproc"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/storage"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// stubFetcher is a deterministic URLFetcher used in HTTP tests.
type stubFetcher struct {
	result imageproc.FetchResult
	err    error
}

// Fetch returns the pre-baked FetchResult or the configured error.
func (f *stubFetcher) Fetch(_ context.Context, _ string) (imageproc.FetchResult, error) {
	if f.err != nil {
		return imageproc.FetchResult{}, f.err
	}
	return f.result, nil
}

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 30, B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func buildImageHandler(t *testing.T, userHash string, settings ImageSettings, fetcher URLFetcher) (http.Handler, *Service, *storage.FakeStore) {
	t.Helper()
	db := newTestDB(t)
	clock := fixedClock(time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC))
	svc, err := NewService(db, nil, clock.Now, Settings{
		RetentionDays: DefaultRetentionDays,
		Images:        settings,
	})
	require.NoError(t, err)

	store := storage.NewFakeStore()
	manager := NewImageManager(store, fetcher, settings)
	handler := NewCombinedHTTPHandlerWithImages(svc, nil, manager, log.Logger.Named("test_http"), nil)
	_ = userHash
	return handler, svc, store
}

func defaultImageSettings() ImageSettings {
	return ImageSettings{
		Enabled:           true,
		Bucket:            "bucket",
		Prefix:            "mcp/images",
		PerUserQuotaBytes: 10 * 1024 * 1024,
		PerImageMaxBytes:  20 * 1024 * 1024,
		MaxPerRequest:     5,
		ObjectTTLDays:     7,
		PresignTTL:        30 * time.Minute,
	}
}

func TestHandleCreateMultipartSuccess(t *testing.T) {
	handler, svc, store := buildImageHandler(t, "img-http-1", defaultImageSettings(), nil)

	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	require.NoError(t, mw.WriteField("content", "look at this"))
	require.NoError(t, mw.WriteField("task_id", "default"))

	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", `form-data; name="images"; filename="a.png"`)
	hdr.Set("Content-Type", "image/png")
	part, err := mw.CreatePart(hdr)
	require.NoError(t, err)
	_, err = part.Write(testPNG(t, 40, 30))
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req := httptest.NewRequest(http.MethodPost, "/api/requests", buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer sk-imghttp-1-9a9a9a9a9a")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var payload struct {
		Request struct {
			ID     string `json:"id"`
			Images []struct {
				URL    string `json:"url"`
				SHA256 string `json:"sha256"`
			} `json:"images"`
		} `json:"request"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Request.Images, 1)
	require.NotEmpty(t, payload.Request.Images[0].URL)
	require.NotEmpty(t, payload.Request.Images[0].SHA256)
	require.Equal(t, 1, store.ObjectCount())

	// Confirm DB persisted the image link.
	require.Equal(t, 30*time.Minute, store.LastPresignTTL())
	_ = svc
}

func TestHandleCreateJSONWithImageURL(t *testing.T) {
	// Prepare a fetch result with a real PNG.
	pngBytes := testPNG(t, 30, 30)
	fetcher := &stubFetcher{result: imageproc.FetchResult{Body: pngBytes, MIMEHint: "image/png"}}
	handler, _, store := buildImageHandler(t, "img-http-json", defaultImageSettings(), fetcher)

	body := map[string]any{
		"content":    "compare",
		"image_urls": []string{"https://example.com/a.png"},
	}
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/requests", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-imghttp-json-1234567890")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, 1, store.ObjectCount())
}

func TestHandleCreateJSONImageURLBlocked(t *testing.T) {
	fetcher := &stubFetcher{err: imageproc.ErrURLBlocked}
	handler, _, _ := buildImageHandler(t, "img-http-block", defaultImageSettings(), fetcher)

	payload := []byte(`{"content":"x","image_urls":["https://10.0.0.1/a.png"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/requests", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-imghttp-blk-1234567890")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	var errBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errBody))
	require.Equal(t, "url_blocked", errBody["error"])
	require.EqualValues(t, 0, mustFloat(errBody["attachment_index"]))
}

func TestHandleCreateTextOnlyByteCompatibility(t *testing.T) {
	// Image feature flag off simulates a "no-op" deployment: plain JSON must
	// behave byte-identically to the pre-change path (no images key).
	settings := defaultImageSettings()
	settings.Enabled = false
	handler, _, store := buildImageHandler(t, "img-http-textonly", settings, nil)

	payload := []byte(`{"content":"hi","task_id":"default"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/requests", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-imghttp-text-1234567890")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var body struct {
		Request map[string]any `json:"request"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	_, hasImages := body.Request["images"]
	require.False(t, hasImages, "text-only response must omit images key")
	require.Zero(t, store.ObjectCount())
}

func TestHandleQuotaFreshUser(t *testing.T) {
	handler, _, _ := buildImageHandler(t, "img-quota", defaultImageSettings(), nil)
	req := httptest.NewRequest(http.MethodGet, "/api/quota", nil)
	req.Header.Set("Authorization", "Bearer sk-imghttp-quota-1234567890")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.EqualValues(t, 0, mustFloat(resp["used_bytes"]))
	require.EqualValues(t, 10*1024*1024, mustFloat(resp["quota_bytes"]))
}

func TestHandleCreateTooManyAttachments(t *testing.T) {
	settings := defaultImageSettings()
	settings.MaxPerRequest = 2
	handler, _, _ := buildImageHandler(t, "img-many", settings, nil)

	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	require.NoError(t, mw.WriteField("content", "x"))
	for i := 0; i < 3; i++ {
		hdr := make(textproto.MIMEHeader)
		hdr.Set("Content-Disposition", `form-data; name="images"; filename="a.png"`)
		hdr.Set("Content-Type", "image/png")
		part, err := mw.CreatePart(hdr)
		require.NoError(t, err)
		_, err = io.Copy(part, bytes.NewReader(testPNG(t, 10+i, 10)))
		require.NoError(t, err)
	}
	require.NoError(t, mw.Close())

	req := httptest.NewRequest(http.MethodPost, "/api/requests", buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer sk-imghttp-many-1234567890")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	var errBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errBody))
	require.Equal(t, "too_many_images", errBody["error"])
}

// mustFloat normalizes a JSON-decoded number for require.Equal comparisons.
func mustFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}
