package userrequests

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// countingBody counts the bytes the handler reads from a request body.
type countingBody struct {
	r    io.Reader
	read atomic.Int64
}

// Read implements io.Reader.
func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.read.Add(int64(n))
	return n, err
}

// multipartBody builds a create request body carrying files as image parts.
func multipartBody(t *testing.T, files ...[]byte) (*bytes.Buffer, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	require.NoError(t, mw.WriteField("content", "see attached"))
	for i, file := range files {
		hdr := make(textproto.MIMEHeader)
		hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="images"; filename="img%d.png"`, i))
		hdr.Set("Content-Type", "image/png")
		part, err := mw.CreatePart(hdr)
		require.NoError(t, err)
		_, err = part.Write(file)
		require.NoError(t, err)
	}
	require.NoError(t, mw.Close())
	return buf, mw.FormDataContentType()
}

// TestHandleCreateAdmitsBeforeReadingMultipartBody is the review's P2 on
// admission order: the handler read and buffered every attachment before it
// took an image-create slot, so waiting requests already held their input
// bytes and the create cap did not bound image-request memory. A request
// that cannot get a slot must be refused without reading its body.
func TestHandleCreateAdmitsBeforeReadingMultipartBody(t *testing.T) {
	settings := defaultImageSettings()
	settings.MaxConcurrentCreates = 1
	h := newOrphanHarness(t, settings, "")
	release, err := h.images.AdmitCreate(context.Background(), mustAuth(t, "sk-admit-holder-1234567"))
	require.NoError(t, err)
	defer release()

	raw, contentType := multipartBody(t, testPNG(t, 40, 30))
	body := &countingBody{r: raw}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/requests", body).WithContext(ctx)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer sk-admit-waiter-1234567")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, "image_busy", errorCode(t, rec))
	require.Zero(t, body.read.Load(), "a request that was not admitted must not have its attachments read")
	require.Empty(t, h.store.PutKeys())
}

// TestHandleCreateReadsAttachmentsOneAtATime: once admitted, attachments are
// read from the parsed form one at a time during normalization, so a
// request holds at most one raw attachment in memory besides the bounded
// multipart buffer. Behavior and stored bytes are unchanged.
func TestHandleCreateReadsAttachmentsOneAtATime(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	rec := h.post(t, context.Background(), "sk-admit-lazy-read-12345", testPNG(t, 40, 30), testPNG(t, 31, 17))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Len(t, h.store.Keys(), 2)
	h.requireNoUnreferencedObjects(t)
}

// TestImageCreateLimiterBoundsWaiters is the review's P2 on the unbounded
// queue: requests over the cap waited without limit. The queue is bounded
// overall and per identity; a request beyond it is refused immediately.
func TestImageCreateLimiterBoundsWaiters(t *testing.T) {
	acquireAsync := func(l *imageCreateLimiter, ctx context.Context, user string) chan error {
		done := make(chan error, 1)
		go func() {
			release, err := l.acquire(ctx, user)
			if err == nil {
				defer release()
				<-ctx.Done()
			}
			done <- err
		}()
		return done
	}
	waitQueued := func(t *testing.T, l *imageCreateLimiter, n int) {
		t.Helper()
		require.Eventually(t, func() bool {
			l.mu.Lock()
			defer l.mu.Unlock()
			return len(l.waiters) == n
		}, time.Second, time.Millisecond)
	}
	refusedNow := func(t *testing.T, l *imageCreateLimiter, user string) {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			_, err := l.acquire(context.Background(), user)
			done <- err
		}()
		select {
		case err := <-done:
			require.ErrorIs(t, err, ErrImageBusy)
		case <-time.After(time.Second):
			t.Fatalf("acquire for %q waited although the queue is full", user)
		}
	}

	t.Run("global queue", func(t *testing.T) {
		l := newImageCreateLimiter(1, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		holder, ok := tryAcquire(t, l, "a")
		require.True(t, ok)
		defer holder()
		waiter := acquireAsync(l, ctx, "b")
		waitQueued(t, l, 1)
		refusedNow(t, l, "c")
		cancel()
		require.ErrorIs(t, <-waiter, ErrImageBusy)
	})

	t.Run("per identity queue", func(t *testing.T) {
		l := newImageCreateLimiter(4, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		holder, ok := tryAcquire(t, l, "a")
		require.True(t, ok)
		defer holder()
		waiter := acquireAsync(l, ctx, "a")
		waitQueued(t, l, 1)
		refusedNow(t, l, "a")
		other, ok := tryAcquire(t, l, "b")
		require.True(t, ok, "another identity is still admitted")
		other()
		cancel()
		require.ErrorIs(t, <-waiter, ErrImageBusy)
	})
}
