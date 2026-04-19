package userrequests

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	errs "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// newImageTestService constructs a Service with a fresh sqlite DB and
// image-aware settings wired up. userIdentitySuffix is appended to the test
// user identity so concurrent tests using the shared sqlite cache do not
// interfere with each other.
func newImageTestService(t *testing.T, userIdentitySuffix string) (*Service, context.Context) {
	t.Helper()
	db := newTestDB(t)
	clock := fixedClock(time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC))
	svc, err := NewService(db, nil, clock.Now, Settings{
		RetentionDays: DefaultRetentionDays,
		Images: ImageSettings{
			Enabled:           true,
			Bucket:            "bucket",
			Prefix:            "mcp/images",
			PerUserQuotaBytes: 1 << 20, // 1 MiB for easier quota tests
			PerImageMaxBytes:  20 * 1024 * 1024,
			MaxPerRequest:     5,
			ObjectTTLDays:     7,
			PresignTTL:        30 * time.Minute,
		},
	})
	require.NoError(t, err)
	return svc, context.Background()
}

func makeUploadedImage(sha string, size int64) UploadedImage {
	return UploadedImage{
		SHA256:       sha,
		StorageKey:   "mcp/images/user/" + sha + ".png",
		PNG:          []byte("fake"),
		SizeBytes:    size,
		Width:        800,
		Height:       600,
		OriginalMIME: "image/png",
	}
}

func TestCreateRequestWithImages_TextOnly(t *testing.T) {
	svc, ctx := newImageTestService(t, "textonly")
	auth := testAuth("imgtext-1", "aaaa")
	req, err := svc.CreateRequestWithImages(ctx, auth, "hello", "", nil)
	require.NoError(t, err)
	require.Empty(t, req.Images)
	require.Equal(t, "hello", req.Content)
}

func TestCreateRequestWithImages_HappyPath(t *testing.T) {
	svc, ctx := newImageTestService(t, "happy")
	auth := testAuth("imgok-1", "aaaa")

	images := []UploadedImage{
		makeUploadedImage(strings.Repeat("a", 64), 1000),
		makeUploadedImage(strings.Repeat("b", 64), 2000),
	}
	req, err := svc.CreateRequestWithImages(ctx, auth, "analyze these", "", images)
	require.NoError(t, err)
	require.Len(t, req.Images, 2)

	consumed, err := svc.ConsumeAllPending(ctx, auth, "")
	require.NoError(t, err)
	require.Len(t, consumed, 1)
	require.Len(t, consumed[0].Images, 2)
	require.Equal(t, 0, consumed[0].Images[0].SortOrder)
	require.Equal(t, 1, consumed[0].Images[1].SortOrder)
}

func TestCreateRequestWithImages_ImageOnly(t *testing.T) {
	svc, ctx := newImageTestService(t, "imageonly")
	auth := testAuth("imgonly-1", "aaaa")

	img := makeUploadedImage(strings.Repeat("c", 64), 500)
	req, err := svc.CreateRequestWithImages(ctx, auth, "", "", []UploadedImage{img})
	require.NoError(t, err)
	require.Empty(t, req.Content)
	require.Len(t, req.Images, 1)
}

func TestCreateRequestWithImages_TooMany(t *testing.T) {
	svc, ctx := newImageTestService(t, "toomany")
	auth := testAuth("imgmany-1", "aaaa")
	images := make([]UploadedImage, 6)
	for i := range images {
		images[i] = makeUploadedImage(fmt.Sprintf("%064d", i), 10)
	}
	_, err := svc.CreateRequestWithImages(ctx, auth, "x", "", images)
	require.Error(t, err)
	require.True(t, errs.Is(err, ErrTooManyImages))
}

func TestCreateRequestWithImages_QuotaExceeded(t *testing.T) {
	svc, ctx := newImageTestService(t, "quota")
	auth := testAuth("imgquota-1", "aaaa")

	// Fill quota to 1 MiB with two distinct images.
	first := makeUploadedImage(strings.Repeat("d", 64), 600*1024)
	second := makeUploadedImage(strings.Repeat("e", 64), 500*1024)
	_, err := svc.CreateRequestWithImages(ctx, auth, "ok", "", []UploadedImage{first})
	require.NoError(t, err)
	_, err = svc.CreateRequestWithImages(ctx, auth, "ok", "", []UploadedImage{second})
	require.Error(t, err)
	require.True(t, errs.Is(err, ErrQuotaExceeded))
}

func TestCreateRequestWithImages_DedupRefreshesTTL(t *testing.T) {
	svc, ctx := newImageTestService(t, "dedup")
	auth := testAuth("imgdedup-1", "aaaa")

	img := makeUploadedImage(strings.Repeat("f", 64), 100)
	_, err := svc.CreateRequestWithImages(ctx, auth, "first", "", []UploadedImage{img})
	require.NoError(t, err)

	usage, err := svc.QuotaUsage(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, int64(100), usage.UsedBytes)
	require.Equal(t, int64(1), usage.ObjectCount)

	// Re-uploading the same SHA should not charge quota again.
	_, err = svc.CreateRequestWithImages(ctx, auth, "second", "", []UploadedImage{img})
	require.NoError(t, err)
	usage, err = svc.QuotaUsage(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, int64(100), usage.UsedBytes, "dedup did not re-charge quota")
	require.Equal(t, int64(1), usage.ObjectCount)
}

func TestGCExpiredImageRefs(t *testing.T) {
	svc, ctx := newImageTestService(t, "gc")
	auth := testAuth("imggc-1", "aaaa")

	img := makeUploadedImage(strings.Repeat("7", 64), 100)
	_, err := svc.CreateRequestWithImages(ctx, auth, "x", "", []UploadedImage{img})
	require.NoError(t, err)

	// Fast-forward the service clock past the 7-day TTL.
	svc.clock = func() time.Time { return time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC) }

	n, err := svc.GCExpiredImageRefs(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	usage, err := svc.QuotaUsage(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, int64(0), usage.UsedBytes)
	require.Equal(t, int64(0), usage.ObjectCount)
}

func TestQuotaUsageIgnoresExpired(t *testing.T) {
	svc, ctx := newImageTestService(t, "expusage")
	auth := testAuth("imgexp-1", "aaaa")

	img := makeUploadedImage(strings.Repeat("8", 64), 100)
	_, err := svc.CreateRequestWithImages(ctx, auth, "x", "", []UploadedImage{img})
	require.NoError(t, err)

	// Forward the clock well past expiry.
	svc.clock = func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }
	usage, err := svc.QuotaUsage(ctx, auth)
	require.NoError(t, err)
	require.Equal(t, int64(0), usage.UsedBytes)
}
