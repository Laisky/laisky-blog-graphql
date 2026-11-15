package cmd

import (
	"context"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/imageproc"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/storage"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
)

// buildUserRequestImageManager wires the ImageManager used by the user-requests
// HTTP handler and the MCP tool. It returns ErrImageFeatureDisabled when the feature flag is off.
// Production builds exactly one per process, so its decode budget and create
// limits are process-wide. The bucket lifecycle rule is checked (and, in
// manage mode, installed) by ImageManager.StartLifecycleController, which the
// caller starts with the other image workers; a missing rule does not prevent
// the service from starting (local dev MinIOs frequently lack permission) but
// is logged at ERROR (see "Image Messages" in docs/manual/mcp.md).
func buildUserRequestImageManager(ctx context.Context, svc *userrequests.Service, logger logSDK.Logger) (*userrequests.ImageManager, error) {
	settings := svc.ImageSettings()
	if !settings.Enabled {
		return nil, errors.WithStack(userrequests.ErrImageFeatureDisabled)
	}

	store, err := storage.NewMinIOClient(storage.MinIOConfig{
		Endpoint:  settings.Endpoint,
		Bucket:    settings.Bucket,
		AccessKey: settings.AccessKey,
		SecretKey: settings.SecretKey,
		UseSSL:    settings.UseSSL,
	})
	if err != nil {
		return nil, errors.Wrap(err, "build minio client")
	}

	if probeErr := store.VerifyBucket(ctx); probeErr != nil {
		logger.Warn("minio bucket probe failed; image uploads will error until resolved",
			zap.String("endpoint", settings.Endpoint),
			zap.String("bucket", settings.Bucket),
			zap.Error(probeErr),
		)
	}

	fetcher := imageproc.NewURLFetcher(imageproc.URLFetchConfig{
		AllowHTTP:    settings.URLAllowHTTP,
		MaxRedirects: settings.URLMaxRedirects,
		TotalTimeout: settings.URLTotalTimeout,
		MaxBodyBytes: settings.PerImageMaxBytes,
	})

	return userrequests.NewImageManager(store, fetcher, settings), nil
}
