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

// TestHandleCreateSlowBodyReleasesSlotAtDeadline (second review): the create
// slot is now taken before the multipart body is read, and body reads do not
// watch the request context, so a client trickling its body could keep the
// slot past the handler's deadline indefinitely. The body read must end at
// the request deadline and give the slot back.
func TestHandleCreateSlowBodyReleasesSlotAtDeadline(t *testing.T) {
	prev := createRequestTimeout
	createRequestTimeout = 300 * time.Millisecond
	t.Cleanup(func() { createRequestTimeout = prev })

	settings := defaultImageSettings()
	settings.MaxConcurrentCreates = 1
	h := newOrphanHarness(t, settings, "")
	srv := httptest.NewServer(h.handler)
	t.Cleanup(srv.Close)

	raw, contentType := multipartBody(t, testPNG(t, 40, 30))
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/requests", pr)
	require.NoError(t, err)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer sk-slow-body-sender-12345")
	done := make(chan int, 1)
	go func() {
		resp, err := srv.Client().Do(req)
		if err != nil {
			done <- 0
			return
		}
		_ = resp.Body.Close()
		done <- resp.StatusCode
	}()
	_, err = pw.Write(raw.Bytes()[:64]) // a partial first part, then silence
	require.NoError(t, err)

	other := mustAuth(t, "sk-slow-body-other-123456")
	admit := func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		release, err := h.images.AdmitCreate(ctx, other)
		if err != nil {
			return false
		}
		release()
		return true
	}
	require.Eventually(t, func() bool { return !admit() }, time.Second, 5*time.Millisecond,
		"fixture: the slow request must hold the only slot")
	require.Eventually(t, admit, 2*time.Second, 20*time.Millisecond,
		"the slot must be released once the slow request's deadline passes")
	_ = pw.Close()
	require.Equal(t, http.StatusServiceUnavailable, <-done, "the timed-out body read is reported as image_busy, not image_too_large")
}
