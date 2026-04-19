package userrequests

import (
	"fmt"
	"strings"
	"time"

	gconfig "github.com/Laisky/go-config/v2"
)

// Default values used when the corresponding config key is absent. Every
// default is also echoed in the comment of the field that consumes it so
// operators can see it without opening this file.
const (
	// DefaultRetentionDays is the fallback TTL (days) for stored user request rows.
	// Default: 30.
	DefaultRetentionDays = 30
	// DefaultRetentionSweepInterval is how often the retention pruner wakes up.
	// Default: 6h.
	DefaultRetentionSweepInterval = 6 * time.Hour

	// DefaultImagePerUserQuotaBytes is the per-user image quota. Default: 100 MiB.
	DefaultImagePerUserQuotaBytes int64 = 100 * 1024 * 1024
	// DefaultImagePerImageMaxBytes is the per-image raw byte cap. Default: 20 MiB.
	DefaultImagePerImageMaxBytes int64 = 20 * 1024 * 1024
	// DefaultImageMaxPerRequest is the max number of attachments per request. Default: 5.
	DefaultImageMaxPerRequest = 5
	// DefaultImageObjectTTLDays is how many days a MinIO object lives. Default: 7.
	DefaultImageObjectTTLDays = 7
	// DefaultImagePresignTTLMinutes is the presigned-URL lifetime. Default: 30 (min).
	DefaultImagePresignTTLMinutes = 30
	// DefaultImageGCSweepInterval is how often the DB-side image GC runs. Default: 1h.
	DefaultImageGCSweepInterval = time.Hour

	// DefaultImageMinIOEndpoint is the endpoint the MinIO client uses when one is
	// not provided. Default: "s3.laisky.com".
	DefaultImageMinIOEndpoint = "s3.laisky.com"
	// DefaultImageMinIOPrefix is the object-key prefix under the bucket. Default:
	// "mcp/user_requests/images".
	DefaultImageMinIOPrefix = "mcp/user_requests/images"
	// DefaultImageMinIOUseSSL toggles HTTPS for the MinIO client. Default: true.
	DefaultImageMinIOUseSSL = true

	// DefaultImageURLAllowHTTP controls whether http:// image URLs are followed.
	// Default: false (HTTPS only).
	DefaultImageURLAllowHTTP = false
	// DefaultImageURLMaxRedirects caps redirect hops for image_url fetches.
	// Default: 3.
	DefaultImageURLMaxRedirects = 3
	// DefaultImageURLTotalTimeoutSeconds is the overall fetch deadline in seconds.
	// Default: 15.
	DefaultImageURLTotalTimeoutSeconds = 15

	// defaultStoredImageContentType is the Content-Type written to MinIO. Not
	// user-configurable because the pipeline always re-encodes to PNG.
	defaultStoredImageContentType = "image/png"
)

// Settings holds runtime configuration for the user requests service.
type Settings struct {
	// RetentionDays is how long user request rows are kept before TTL pruning.
	// Non-positive values fall back to DefaultRetentionDays (30 days).
	RetentionDays int
	// RetentionSweepInterval is how often the retention pruner runs.
	// Zero or negative disables the worker. Default: 6h.
	RetentionSweepInterval time.Duration
	// Images groups every knob governing image attachments. See ImageSettings.
	Images ImageSettings
}

// ImageSettings groups every knob governing image attachments. All fields
// except AccessKey / SecretKey / Bucket have sensible defaults; the three
// credential-adjacent fields are required only when Enabled is true.
type ImageSettings struct {
	// Enabled turns the entire image subsystem on or off. When false the
	// compose UI hides image controls, multipart POSTs return
	// 415 feature_disabled, and MCP responses remain byte-identical to the
	// pure-text shape. Default: false.
	Enabled bool
	// Bucket is the MinIO bucket that owns image objects. Required when
	// Enabled=true; no default because buckets are deployment-specific.
	Bucket string
	// Prefix is the object-key prefix under the bucket. Default:
	// "mcp/user_requests/images". Leading / trailing slashes are trimmed.
	Prefix string
	// Endpoint is the MinIO host (no scheme). Default: "s3.laisky.com".
	Endpoint string
	// AccessKey is the MinIO access key. Required when Enabled=true.
	AccessKey string
	// SecretKey is the MinIO secret key. Required when Enabled=true.
	SecretKey string
	// UseSSL selects HTTPS vs HTTP against Endpoint. Default: true.
	UseSSL bool
	// PerUserQuotaBytes caps the total post-normalization storage a single
	// user may occupy across live (non-expired) objects. Default: 100 MiB.
	PerUserQuotaBytes int64
	// PerImageMaxBytes is the hard per-image raw byte cap applied before
	// decode. Applies uniformly to uploaded files and fetched URLs.
	// Default: 20 MiB.
	PerImageMaxBytes int64
	// MaxPerRequest is the maximum number of attachments per request,
	// counting files and image_urls together. Default: 5.
	MaxPerRequest int
	// ObjectTTLDays is how many days a MinIO object lives before the bucket
	// lifecycle rule deletes it. Re-uploading the same SHA refreshes the TTL.
	// Default: 7.
	ObjectTTLDays int
	// PresignTTL is the validity window of presigned GET URLs handed to AI
	// agents. Default: 30m.
	PresignTTL time.Duration
	// GCSweepInterval is how often the DB-side image GC sweeps expired refs.
	// Zero or negative disables the worker. Default: 1h.
	GCSweepInterval time.Duration
	// URLAllowHTTP allows unencrypted http:// image_url inputs. Default: false
	// (HTTPS only). Not recommended for production.
	URLAllowHTTP bool
	// URLMaxRedirects caps redirect hops for image_url fetches. Default: 3.
	URLMaxRedirects int
	// URLTotalTimeout is the overall deadline for one image_url fetch
	// (headers + body). Default: 15s.
	URLTotalTimeout time.Duration
	// ContentTypeDefault is the MinIO Content-Type written for stored
	// objects. Always "image/png" — the pipeline re-encodes every input.
	ContentTypeDefault string
}

// LoadSettingsFromConfig populates Settings from the shared configuration
// with the defaults documented on each field.
func LoadSettingsFromConfig() Settings {
	retentionDays := intFromConfig("settings.mcp.user_requests.retention_days", DefaultRetentionDays)
	intervalSeconds := intFromConfig("settings.mcp.user_requests.retention_sweep_seconds", int(DefaultRetentionSweepInterval/time.Second))
	return Settings{
		RetentionDays:          retentionDays,
		RetentionSweepInterval: time.Duration(intervalSeconds) * time.Second,
		Images:                 loadImageSettings(),
	}
}

// loadImageSettings reads the mcp.user_requests.images.* config keys using
// the Default* constants as fallbacks.
func loadImageSettings() ImageSettings {
	return ImageSettings{
		Enabled:            boolFromConfig("settings.mcp.user_requests.images.enabled", false),
		Bucket:             stringFromConfig("settings.mcp.user_requests.images.minio.bucket", ""),
		Prefix:             strings.Trim(stringFromConfig("settings.mcp.user_requests.images.minio.prefix", DefaultImageMinIOPrefix), "/"),
		Endpoint:           stringFromConfig("settings.mcp.user_requests.images.minio.endpoint", DefaultImageMinIOEndpoint),
		AccessKey:          stringFromConfig("settings.mcp.user_requests.images.minio.access_key", ""),
		SecretKey:          stringFromConfig("settings.mcp.user_requests.images.minio.secret_key", ""),
		UseSSL:             boolFromConfig("settings.mcp.user_requests.images.minio.use_ssl", DefaultImageMinIOUseSSL),
		PerUserQuotaBytes:  int64FromConfig("settings.mcp.user_requests.images.per_user_quota_bytes", DefaultImagePerUserQuotaBytes),
		PerImageMaxBytes:   int64FromConfig("settings.mcp.user_requests.images.per_image_max_bytes", DefaultImagePerImageMaxBytes),
		MaxPerRequest:      intFromConfig("settings.mcp.user_requests.images.max_per_request", DefaultImageMaxPerRequest),
		ObjectTTLDays:      intFromConfig("settings.mcp.user_requests.images.object_ttl_days", DefaultImageObjectTTLDays),
		PresignTTL:         time.Duration(intFromConfig("settings.mcp.user_requests.images.presign_ttl_minutes", DefaultImagePresignTTLMinutes)) * time.Minute,
		GCSweepInterval:    time.Duration(intFromConfig("settings.mcp.user_requests.images.gc_sweep_seconds", int(DefaultImageGCSweepInterval/time.Second))) * time.Second,
		URLAllowHTTP:       boolFromConfig("settings.mcp.user_requests.images.url_fetch.allow_http", DefaultImageURLAllowHTTP),
		URLMaxRedirects:    intFromConfig("settings.mcp.user_requests.images.url_fetch.max_redirects", DefaultImageURLMaxRedirects),
		URLTotalTimeout:    time.Duration(intFromConfig("settings.mcp.user_requests.images.url_fetch.total_timeout_seconds", DefaultImageURLTotalTimeoutSeconds)) * time.Second,
		ContentTypeDefault: defaultStoredImageContentType,
	}
}

// intFromConfig retrieves an integer configuration value, returning def when
// the key is absent or the value cannot be coerced to an int.
func intFromConfig(key string, def int) int {
	value := gconfig.S.Get(key)
	switch v := value.(type) {
	case nil:
		return def
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		var parsed int
		if _, err := fmt.Sscanf(v, "%d", &parsed); err == nil {
			return parsed
		}
		return def
	default:
		return def
	}
}

// int64FromConfig retrieves an int64 configuration value, returning def when
// the key is absent or the value cannot be coerced.
func int64FromConfig(key string, def int64) int64 {
	value := gconfig.S.Get(key)
	switch v := value.(type) {
	case nil:
		return def
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		var parsed int64
		if _, err := fmt.Sscanf(v, "%d", &parsed); err == nil {
			return parsed
		}
		return def
	default:
		return def
	}
}

// boolFromConfig retrieves a boolean configuration value. Strings "true",
// "1", "yes", "on" (case-insensitive) resolve to true; the inverse set
// resolves to false; everything else returns def.
func boolFromConfig(key string, def bool) bool {
	value := gconfig.S.Get(key)
	switch v := value.(type) {
	case nil:
		return def
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		}
	}
	return def
}

// stringFromConfig retrieves a string configuration value, returning def when
// the key is absent.
func stringFromConfig(key string, def string) string {
	value := gconfig.S.Get(key)
	switch v := value.(type) {
	case nil:
		return def
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}
