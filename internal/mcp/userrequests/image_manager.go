package userrequests

import (
	"bytes"
	"context"
	"fmt"
	"time"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/imageproc"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/storage"
)

// URLFetcher is the subset of imageproc.URLFetcher the manager depends on.
// Exposing it as an interface lets tests substitute a deterministic fake.
type URLFetcher interface {
	Fetch(ctx context.Context, url string) (imageproc.FetchResult, error)
}

// ImageManager turns raw multipart / URL inputs into normalized images and
// owns the object-store side of their lifecycle. It bundles the object store,
// URL fetcher, decode budget, and image settings so HTTP handlers stay thin.
type ImageManager struct {
	store    storage.ObjectStore
	fetcher  URLFetcher
	settings ImageSettings
	budget   *imageproc.DecodeBudget
}

// NewImageManager constructs an ImageManager. store is required; fetcher may
// be nil when image_url inputs are not accepted (e.g. in tests that only feed
// uploaded files). Every image the manager normalizes is admitted through one
// decode budget sized by settings.DecodeMemoryBudgetBytes.
func NewImageManager(store storage.ObjectStore, fetcher URLFetcher, settings ImageSettings) *ImageManager {
	return &ImageManager{
		store:    store,
		fetcher:  fetcher,
		settings: settings,
		budget:   imageproc.NewDecodeBudget(settings.DecodeMemoryBudgetBytes),
	}
}

// AttachmentInput represents one caller-provided attachment — either raw
// bytes from a multipart part or a URL to fetch server-side. Exactly one of
// FileBytes and URL must be non-empty.
type AttachmentInput struct {
	FileBytes []byte
	Filename  string
	URL       string
}

// Prepare fetches and normalizes every attachment and derives its object key.
// It has no side effects on the object store or the database: the objects are
// uploaded only by Service.CreateRequestWithImages, after the quota admitted
// them, so a rejected attachment never leaves an object behind.
func (m *ImageManager) Prepare(ctx context.Context, auth *askuser.AuthorizationContext, inputs []AttachmentInput) ([]UploadedImage, error) {
	if m == nil {
		return nil, errors.WithStack(ErrImageFeatureDisabled)
	}
	if !m.settings.Enabled {
		return nil, errors.WithStack(ErrImageFeatureDisabled)
	}
	if m.settings.MaxPerRequest > 0 && len(inputs) > m.settings.MaxPerRequest {
		return nil, errors.WithStack(ErrTooManyImages)
	}
	if auth == nil {
		return nil, errors.WithStack(ErrInvalidAuthorization)
	}

	out := make([]UploadedImage, 0, len(inputs))
	for idx, in := range inputs {
		img, err := m.prepareOne(ctx, auth, in)
		if err != nil {
			return nil, errors.Wrapf(err, "attachment index %d", idx)
		}
		out = append(out, img)
	}
	return out, nil
}

func (m *ImageManager) prepareOne(ctx context.Context, auth *askuser.AuthorizationContext, in AttachmentInput) (UploadedImage, error) {
	raw := in.FileBytes
	filename := in.Filename
	sourceURL := ""
	if len(raw) == 0 && in.URL != "" {
		if m.fetcher == nil {
			return UploadedImage{}, errors.Wrap(imageproc.ErrURLFetchFailed, "url fetcher unavailable")
		}
		res, err := m.fetcher.Fetch(ctx, in.URL)
		if err != nil {
			return UploadedImage{}, errors.WithStack(err)
		}
		raw = res.Body
		sourceURL = in.URL
		if filename == "" {
			filename = in.URL
		}
	}

	if int64(len(raw)) > m.settings.PerImageMaxBytes {
		return UploadedImage{}, errors.WithStack(imageproc.ErrImageTooLarge)
	}
	if len(raw) == 0 {
		return UploadedImage{}, errors.Wrap(imageproc.ErrDecodeFailed, "empty attachment")
	}

	result, err := m.budget.Normalize(ctx, raw, filename)
	if err != nil {
		return UploadedImage{}, errors.WithStack(err)
	}

	return UploadedImage{
		SHA256:       result.SHA256,
		StorageKey:   m.buildKey(auth.UserIdentity, result.SHA256),
		PNG:          result.PNG,
		SizeBytes:    int64(len(result.PNG)),
		Width:        result.Width,
		Height:       result.Height,
		OriginalMIME: result.OriginalMIME,
		SourceURL:    sourceURL,
	}, nil
}

// PutImage uploads one normalized image under its StorageKey. It implements
// ImageObjectWriter for Service.CreateRequestWithImages.
func (m *ImageManager) PutImage(ctx context.Context, auth *askuser.AuthorizationContext, image UploadedImage) error {
	if m == nil || m.store == nil {
		return errors.Wrap(ErrStorageUnavailable, "image manager unconfigured")
	}
	meta := map[string]string{
		"x-amz-meta-api-key-hash":  auth.APIKeyHash,
		"x-amz-meta-original-mime": image.OriginalMIME,
		"x-amz-meta-uploaded-at":   time.Now().UTC().Format(time.RFC3339),
	}
	if err := m.store.Put(ctx, image.StorageKey, bytes.NewReader(image.PNG), int64(len(image.PNG)), defaultStoredImageContentType, meta); err != nil {
		return errors.Wrap(ErrStorageUnavailable, err.Error())
	}
	return nil
}

// DeleteImage removes one object. It implements ImageObjectWriter; deleting a
// missing key succeeds.
func (m *ImageManager) DeleteImage(ctx context.Context, key string) error {
	if m == nil || m.store == nil {
		return errors.Wrap(ErrStorageUnavailable, "image manager unconfigured")
	}
	if err := m.store.Delete(ctx, key); err != nil {
		return errors.Wrap(err, "delete image object")
	}
	return nil
}

// buildKey returns the object key for a user / sha pair.
func (m *ImageManager) buildKey(userIdentity, sha string) string {
	return fmt.Sprintf("%s/%s/%s.png", m.settings.Prefix, sanitizeKeySegment(userIdentity), sha)
}

// sanitizeKeySegment collapses characters that are unsafe inside an S3 key into dashes.
// The sanitized form preserves identifying features (e.g. email-like user IDs)
// without introducing traversal or ambiguous separators.
func sanitizeKeySegment(in string) string {
	out := make([]byte, 0, len(in))
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			out = append(out, c)
		case c == '-' || c == '_' || c == '.' || c == '@':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "anon"
	}
	return string(out)
}

// PresignURL returns a freshly-signed URL for an image owned by the caller.
// Keys from other users will be refused.
func (m *ImageManager) PresignURL(ctx context.Context, image RequestImage) (string, error) {
	if m == nil || m.store == nil {
		return "", errors.Wrap(ErrStorageUnavailable, "image manager unconfigured")
	}
	ttl := m.settings.PresignTTL
	if ttl <= 0 {
		ttl = time.Duration(DefaultImagePresignTTLMinutes) * time.Minute
	}
	url, err := m.store.PresignedGet(ctx, image.StorageKey, ttl)
	if err != nil {
		return "", errors.Wrap(err, "presign object")
	}
	return url, nil
}

// FetchInline reads the full object bytes for inlining into an MCP response.
// The caller should only invoke this for images that fit within the inline budget.
func (m *ImageManager) FetchInline(ctx context.Context, image RequestImage) ([]byte, error) {
	if m == nil || m.store == nil {
		return nil, errors.Wrap(ErrStorageUnavailable, "image manager unconfigured")
	}
	reader, _, err := m.store.Get(ctx, image.StorageKey)
	if err != nil {
		return nil, errors.Wrap(err, "get object")
	}
	defer func() { _ = reader.Close() }()
	buf := &bytes.Buffer{}
	if _, err := buf.ReadFrom(reader); err != nil {
		return nil, errors.Wrap(err, "read object body")
	}
	return buf.Bytes(), nil
}

// Settings exposes the image settings for HTTP layer decisions (e.g. max
// attachment count enforcement prior to byte ingestion).
func (m *ImageManager) Settings() ImageSettings {
	if m == nil {
		return ImageSettings{}
	}
	return m.settings
}
