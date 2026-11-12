package dao

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/db/arweave"
)

// fakeArweaveUploader preserves the production Upload contract without network I/O.
type fakeArweaveUploader struct {
	fileID string
	err    error
	calls  int
}

func (f *fakeArweaveUploader) Upload(context.Context, []byte, ...arweave.UploadOption) (string, error) {
	f.calls++
	return f.fileID, f.err
}

type uploadRoundTripper func(*http.Request) (*http.Response, error)

func (f uploadRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type cacheRequest struct {
	method, path, contentType, body string
	ctxErr                          error
}

func uploadCacheFixture(t *testing.T, refused bool) (*minio.Client, *[]cacheRequest) {
	t.Helper()
	previousBucket := gconfig.S.GetString("settings.arweave.s3.bucket")
	previousPrefix := gconfig.S.GetString("settings.arweave.s3.prefix")
	gconfig.S.Set("settings.arweave.s3.bucket", "arweave-cache")
	gconfig.S.Set("settings.arweave.s3.prefix", "files/")
	t.Cleanup(func() {
		gconfig.S.Set("settings.arweave.s3.bucket", previousBucket)
		gconfig.S.Set("settings.arweave.s3.prefix", previousPrefix)
	})
	requests := new([]cacheRequest)
	client, err := minio.New("cache.example.test", &minio.Options{
		Creds:  credentials.NewStaticV4("test-access", "test-secret", ""),
		Region: "us-east-1",
		Transport: uploadRoundTripper(func(r *http.Request) (*http.Response, error) {
			body, readErr := io.ReadAll(r.Body)
			if readErr != nil {
				return nil, readErr
			}
			*requests = append(*requests, cacheRequest{
				method: r.Method, path: r.URL.Path, contentType: r.Header.Get("Content-Type"),
				body: string(body), ctxErr: r.Context().Err(),
			})
			status, responseBody := http.StatusOK, ""
			if refused {
				status = http.StatusForbidden
				responseBody = "<Error><Code>AccessDenied</Code><Message>denied</Message></Error>"
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/xml"}},
				Body: io.NopCloser(strings.NewReader(responseBody)), Request: r}, nil
		}),
	})
	require.NoError(t, err)
	return client, requests
}

func uploadLogger(t *testing.T) (context.Context, *observer.ObservedLogs) {
	t.Helper()
	core, observed := observer.New(zapcore.DebugLevel)
	logger, err := logSDK.NewWithName("upload", logSDK.LevelDebug,
		zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)
	return gmw.SetLogger(context.Background(), logger), observed
}

// TestUploadFileCacheRefusalDoesNotRaceWithCaller waits for all runnable work,
// so the old ERROR-then-INFO path cannot pass by observing only its first log.
func TestUploadFileCacheRefusalDoesNotRaceWithCaller(t *testing.T) {
	testUploadFileCache(t, true)
}

func TestUploadFileCacheSuccess(t *testing.T) {
	testUploadFileCache(t, false)
}

func testUploadFileCache(t *testing.T, refused bool) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		client, requests := uploadCacheFixture(t, refused)
		ctx, observed := uploadLogger(t)
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		upload := &Upload{ar: &fakeArweaveUploader{fileID: "tx-cache-race"}, minio: client}
		fileID, err := upload.UploadFile(ctx, []byte("permanent bytes"), "text/plain")
		require.NoError(t, err)
		require.Equal(t, "tx-cache-race", fileID)
		cancel()
		// The transport is synchronous and contains no blocking operations. Wait
		// therefore drains the entire worker, including every log, before inspection.
		synctest.Wait()
		require.Len(t, *requests, 1)
		require.Equal(t, http.MethodPut, (*requests)[0].method)
		require.Equal(t, "/arweave-cache/files/tx-cache-race", (*requests)[0].path)
		require.Equal(t, "text/plain", (*requests)[0].contentType)
		require.Contains(t, (*requests)[0].body, "permanent bytes")
		require.NoError(t, (*requests)[0].ctxErr, "cache work outlives the request")
		entries := observed.FilterMessage("upload to minio").All()
		require.Len(t, entries, 1, "each completed cache write is reported exactly once")
		level := zapcore.InfoLevel
		if refused {
			level = zapcore.ErrorLevel
		}
		require.Equal(t, level, entries[0].Level)
		require.Equal(t, "files/tx-cache-race", entries[0].ContextMap()["objkey"])
		require.Equal(t, "tx-cache-race", fileID)
		require.NoError(t, err, "background cache failure must not replace the caller result")
	})
}

func TestUploadFileArweaveFailureDoesNotCache(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, requests := uploadCacheFixture(t, false)
		ctx, observed := uploadLogger(t)
		providerErr := errors.New("arweave refused")
		upload := &Upload{ar: &fakeArweaveUploader{err: providerErr}, minio: client}
		fileID, err := upload.UploadFile(ctx, []byte("permanent bytes"), "text/plain")
		require.ErrorIs(t, err, providerErr)
		require.Empty(t, fileID)
		synctest.Wait()
		require.Empty(t, *requests)
		require.Empty(t, observed.FilterMessage("upload to minio").All())
	})
}

func TestNewUploadKeepsNilArdriveNil(t *testing.T) {
	require.Nil(t, NewUpload(nil, nil, nil).ar)
}
