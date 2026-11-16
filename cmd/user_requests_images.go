package cmd

import (
	"context"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/imageproc"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/storage"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
	"github.com/Laisky/laisky-blog-graphql/library/memlimit"
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

	settings = sizeImageMemory(settings, memlimit.Detect(), logger)

	fetcher := imageproc.NewURLFetcher(imageproc.URLFetchConfig{
		AllowHTTP:    settings.URLAllowHTTP,
		MaxRedirects: settings.URLMaxRedirects,
		TotalTimeout: settings.URLTotalTimeout,
		MaxBodyBytes: settings.PerImageMaxBytes,
	})

	manager := userrequests.NewImageManager(store, fetcher, settings)
	manager.ExportMetrics()
	return manager, nil
}

// sizeImageMemory fits the image create path into the process memory limit
// (userrequests.SizeImageMemory) and logs the envelope. A lowered decode
// budget is a WARN and an envelope that still exceeds half the limit an
// ERROR. A container limit without a Go soft memory limit (runAPI sets one
// unless GOMEMLIMIT is in the environment, so this means GOMEMLIMIT=off) is a
// WARN: the collector may then let the heap reach twice the live set, which
// measured 2.0-2.1x the envelope (docs/ref/image_memory_envelope_20261115.md).
func sizeImageMemory(settings userrequests.ImageSettings, limits memlimit.Limits, logger logSDK.Logger) userrequests.ImageSettings {
	limit, source, ok := limits.Effective()
	sized, sizing := userrequests.SizeImageMemory(settings, limit)
	fields := []zap.Field{
		zap.Int64("envelope_bytes", sizing.Envelope.Total()),
		zap.Int64("decode_budget_bytes", sizing.Envelope.DecodeBudget),
		zap.Int64("input_buffer_bytes", sizing.Envelope.InputBuffers),
		zap.Int64("prepared_output_bytes", sizing.Envelope.PreparedOutputs),
		zap.Int64("memory_limit_bytes", limit),
		zap.String("memory_limit_source", source),
		zap.Int("max_concurrent_requests", settings.MaxConcurrentCreates),
	}
	switch {
	case !ok:
		logger.Info("image memory envelope; no process memory limit detected", fields...)
	case sizing.LoweredFrom > 0 && sizing.Fits:
		logger.Warn("decode memory budget lowered to fit half the process memory limit; set decode_memory_budget_bytes to override",
			append(fields, zap.Int64("default_decode_budget_bytes", sizing.LoweredFrom))...)
	case !sizing.Fits:
		logger.Error("image memory envelope exceeds half the process memory limit; lower max_concurrent_requests or decode_memory_budget_bytes",
			append(fields, zap.Int64("lowered_from_bytes", sizing.LoweredFrom))...)
	default:
		logger.Info("image memory envelope fits the process memory limit", fields...)
	}
	if limits.Container > 0 && limits.GoSoftLimit == 0 {
		logger.Warn("no Go soft memory limit is in effect under a container memory limit; set GOMEMLIMIT to about 90% of it so the collector runs before the container is killed",
			zap.Int64("container_memory_limit_bytes", limits.Container),
			zap.String("container_memory_limit_source", limits.ContainerSource),
			zap.Int64("suggested_gomemlimit_bytes", limits.Container/10*9))
	}
	return sized
}
