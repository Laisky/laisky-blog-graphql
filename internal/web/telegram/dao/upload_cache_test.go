package dao

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

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

// fakeArweaveUploader returns a fixed transaction ID without any network call.
type fakeArweaveUploader struct{ fileID string }

// Upload returns the configured transaction ID.
func (f fakeArweaveUploader) Upload(context.Context, []byte, ...arweave.UploadOption) (string, error) {
	return f.fileID, nil
}

// TestUploadFileCacheRefusalDoesNotRaceWithCaller reproduces a data race: the
// minio cache goroutine wrote the function's named return values after the
// caller had already received them. Run with -race; the old code fails here.
// A refused cache write must also be reported as an error, not as success.
func TestUploadFileCacheRefusalDoesNotRaceWithCaller(t *testing.T) {
	var puts atomic.Int64
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts.Add(1)
		}
		// AccessDenied is not retried by minio-go, so the goroutine finishes quickly.
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>denied</Message></Error>`))
	}))
	t.Cleanup(store.Close)

	endpoint, err := url.Parse(store.URL)
	require.NoError(t, err)
	client, err := minio.New(endpoint.Host, &minio.Options{
		Creds:  credentials.NewStaticV4("test-access", "test-secret", ""),
		Region: "us-east-1",
	})
	require.NoError(t, err)

	prevBucket := gconfig.S.GetString("settings.arweave.s3.bucket")
	prevPrefix := gconfig.S.GetString("settings.arweave.s3.prefix")
	gconfig.S.Set("settings.arweave.s3.bucket", "arweave-cache")
	gconfig.S.Set("settings.arweave.s3.prefix", "files/")
	t.Cleanup(func() {
		gconfig.S.Set("settings.arweave.s3.bucket", prevBucket)
		gconfig.S.Set("settings.arweave.s3.prefix", prevPrefix)
	})

	core, observed := observer.New(zapcore.DebugLevel)
	logger, err := logSDK.NewWithName("upload", logSDK.LevelDebug,
		zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)
	ctx := gmw.SetLogger(context.Background(), logger)

	upload := &Upload{ar: fakeArweaveUploader{fileID: "tx-cache-race"}, minio: client}
	fileID, err := upload.UploadFile(ctx, []byte("permanent bytes"), "text/plain")
	require.NoError(t, err)
	require.Equal(t, "tx-cache-race", fileID)

	require.Eventually(t, func() bool {
		return observed.FilterMessage("upload to minio").Len() > 0
	}, 10*time.Second, 10*time.Millisecond, "the cache goroutine must finish")
	require.EqualValues(t, 1, puts.Load(), "the cache write must reach the store")

	entries := observed.FilterMessage("upload to minio").All()
	require.Len(t, entries, 1, "a refused cache write is reported once")
	require.Equal(t, zapcore.ErrorLevel, entries[0].Level, "a refused cache write must not also be logged as a success")
	require.Equal(t, "files/tx-cache-race", entries[0].ContextMap()["objkey"])
	require.Equal(t, "tx-cache-race", fileID, "the caller's result is unchanged by the cache goroutine")
}

// TestNewUploadKeepsNilArdriveNil pins that a missing Ardrive stays a nil
// interface, so the field never hides a nil pointer behind a non-nil value.
func TestNewUploadKeepsNilArdriveNil(t *testing.T) {
	upload := NewUpload(nil, nil, nil)
	require.Nil(t, upload.ar)
}
