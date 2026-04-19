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
// HTTP handler and the MCP tool. It returns nil when the feature flag is off.
// buildUserRequestImageManager also installs the bucket lifecycle rule when the
// feature is enabled; a failure to install the rule is logged but does not
// prevent the service from starting (local dev MinIOs frequently lack
// permission, and the lifecycle is idempotent anyway).
func buildUserRequestImageManager(ctx context.Context, svc *userrequests.Service, logger logSDK.Logger) (*userrequests.ImageManager, error) {
	settings := svc.ImageSettings()
	if !settings.Enabled {
		return nil, nil
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

	if settings.ObjectTTLDays > 0 {
		if lifecycleErr := store.EnsureLifecycle(ctx, settings.Prefix+"/", settings.ObjectTTLDays); lifecycleErr != nil {
			logger.Warn("install bucket lifecycle failed", zap.Error(lifecycleErr))
		}
	}

	fetcher := imageproc.NewURLFetcher(imageproc.URLFetchConfig{
		AllowHTTP:    settings.URLAllowHTTP,
		MaxRedirects: settings.URLMaxRedirects,
		TotalTimeout: settings.URLTotalTimeout,
		MaxBodyBytes: settings.PerImageMaxBytes,
	})

	return userrequests.NewImageManager(store, fetcher, settings), nil
}
