package dao

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"
)

// TestUploadFileWithApikeyFinishesChargedUpload: the OneAPI charge is
// single-phase and never refunded, so once it is accepted the upload must not
// be abandoned when the caller stops waiting, or the user pays for nothing.
func TestUploadFileWithApikeyFinishesChargedUpload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, _ := uploadCacheFixture(t, false)
		ctx, _ := uploadLogger(t)
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		previousClient := http.DefaultClient
		http.DefaultClient = &http.Client{Transport: uploadRoundTripper(func(r *http.Request) (*http.Response, error) {
			// The caller disconnects while the charge is being accepted.
			cancel()
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
		})}
		t.Cleanup(func() { http.DefaultClient = previousClient })

		ar := &fakeArweaveUploader{fileID: "tx-paid"}
		upload := &Upload{ar: ar, minio: client}
		fileID, err := upload.UploadFileWithApikey(ctx, "fixture-key", []byte("paid bytes"), "text/plain")
		require.NoError(t, err)
		require.Equal(t, "tx-paid", fileID)
		require.Error(t, ctx.Err(), "the caller was canceled during billing")
		require.Equal(t, []error{nil}, ar.ctxErrs, "a charged upload must not inherit the caller's cancellation")
		synctest.Wait()
	})
}

// TestUploadFileKeepsCallerCancellation: an unbilled upload has spent nothing
// yet, so it must still see the caller's cancellation.
func TestUploadFileKeepsCallerCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, _ := uploadCacheFixture(t, false)
		ctx, _ := uploadLogger(t)
		ctx, cancel := context.WithCancel(ctx)
		cancel()

		ar := &fakeArweaveUploader{fileID: "tx-unbilled"}
		upload := &Upload{ar: ar, minio: client}
		_, err := upload.UploadFile(ctx, []byte("unbilled bytes"), "text/plain")
		require.NoError(t, err)
		require.Equal(t, []error{context.Canceled}, ar.ctxErrs)
		synctest.Wait()
	})
}
