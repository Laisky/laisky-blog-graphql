package userrequests

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/Laisky/zap"
	"github.com/google/uuid"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
)

// ensureImageSchema installs the tables that back RequestImage. The migration
// is additive — pure-text flows never touch these tables.
func (s *Service) ensureImageSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS mcp_user_image_refs (
			id UUID PRIMARY KEY,
			user_identity VARCHAR(255) NOT NULL,
			api_key_hash CHAR(64) NOT NULL,
			sha256 CHAR(64) NOT NULL,
			storage_key TEXT NOT NULL,
			size_bytes BIGINT NOT NULL,
			width INTEGER NOT NULL,
			height INTEGER NOT NULL,
			mime_type VARCHAR(64) NOT NULL,
			original_mime VARCHAR(64) NOT NULL,
			source_url TEXT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_mcp_user_image_refs_user_sha ON mcp_user_image_refs (user_identity, sha256)`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_user_image_refs_user_expires ON mcp_user_image_refs (user_identity, expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_user_image_refs_expires ON mcp_user_image_refs (expires_at)`,
		`CREATE TABLE IF NOT EXISTS mcp_user_request_image_links (
			request_id UUID NOT NULL,
			image_id UUID NOT NULL,
			sort_order INTEGER NOT NULL,
			PRIMARY KEY (request_id, sort_order)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_user_request_image_links_image ON mcp_user_request_image_links (image_id)`,
	}

	for _, stmt := range append(statements, imageClaimsSchema...) {
		if _, err := s.execContext(ctx, stmt); err != nil {
			return errors.Wrap(err, "apply image schema statement")
		}
	}
	return nil
}

// QuotaUsage reports a user's live image storage footprint.
type QuotaUsage struct {
	UserIdentity string
	UsedBytes    int64
	QuotaBytes   int64
	ObjectCount  int64
	TTLDays      int
}

// QuotaUsage returns the live (non-expired) image byte usage for a user.
func (s *Service) QuotaUsage(ctx context.Context, auth *askuser.AuthorizationContext) (QuotaUsage, error) {
	if auth == nil {
		return QuotaUsage{}, ErrInvalidAuthorization
	}
	now := s.clock()
	row := s.queryRowContext(ctx,
		`SELECT COALESCE(SUM(size_bytes), 0) AS used, COALESCE(COUNT(1), 0) AS n
		   FROM mcp_user_image_refs
		  WHERE user_identity = ? AND expires_at > ?`,
		auth.UserIdentity, now,
	)
	var used, count int64
	if err := row.Scan(&used, &count); err != nil {
		return QuotaUsage{}, errors.Wrap(err, "query quota usage")
	}
	return QuotaUsage{
		UserIdentity: auth.UserIdentity,
		UsedBytes:    used,
		QuotaBytes:   s.settings.Images.PerUserQuotaBytes,
		ObjectCount:  count,
		TTLDays:      s.settings.Images.ObjectTTLDays,
	}, nil
}

// reserveImage performs the upsert-with-quota dance inside the create
// transaction, which already holds the image scope lock (so the usage sum
// cannot race another reservation of the same user). A live row for the same
// SHA is refreshed without charging quota and returned with existed=true. An
// expired row no longer counts toward usage, so reviving it is charged like a
// new image (proposal §3.5) before it is refreshed. Otherwise a new row is
// inserted after verifying the user stays within quota.
func (s *Service) reserveImage(ctx context.Context, tx *sql.Tx, auth *askuser.AuthorizationContext, image UploadedImage) (uuid.UUID, bool, error) {
	now := s.clock()
	ttl := s.settings.Images.ObjectTTLDays
	if ttl <= 0 {
		ttl = DefaultImageObjectTTLDays
	}
	expiresAt := now.Add(time.Duration(ttl) * 24 * time.Hour)

	lockQuery := rebindQuery(
		`SELECT id, size_bytes, expires_at FROM mcp_user_image_refs
		  WHERE user_identity = ? AND sha256 = ? FOR UPDATE`,
		s.useDollar,
	)
	// SQLite does not understand FOR UPDATE; use a portable variant there.
	if !s.useDollar {
		lockQuery = `SELECT id, size_bytes, expires_at FROM mcp_user_image_refs
		              WHERE user_identity = ? AND sha256 = ?`
	}

	row := tx.QueryRowContext(ctx, lockQuery, auth.UserIdentity, image.SHA256)
	var existingID string
	var existingSize int64
	var existingExpiresRaw any
	err := row.Scan(&existingID, &existingSize, &existingExpiresRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, false, errors.Wrap(err, "reserve image lookup")
	}
	if err == nil {
		id, parseErr := uuid.Parse(existingID)
		if parseErr != nil {
			return uuid.Nil, false, errors.Wrap(parseErr, "parse existing image id")
		}
		existingExpires, timeErr := parseSQLTime(existingExpiresRaw)
		if timeErr != nil {
			return uuid.Nil, false, errors.Wrap(timeErr, "parse existing image expires_at")
		}
		if !existingExpires.After(now) {
			if quotaErr := s.checkImageQuota(ctx, tx, auth, now, image.SizeBytes); quotaErr != nil {
				return uuid.Nil, false, errors.WithStack(quotaErr)
			}
		}
		// Refresh the TTL and point the row at the key this request uploads.
		if _, execErr := tx.ExecContext(ctx,
			rebindQuery(`UPDATE mcp_user_image_refs SET expires_at = ?, storage_key = ? WHERE id = ?`, s.useDollar),
			expiresAt, image.StorageKey, existingID,
		); execErr != nil {
			return uuid.Nil, false, errors.Wrap(execErr, "refresh image ttl")
		}
		return id, true, nil
	}

	if quotaErr := s.checkImageQuota(ctx, tx, auth, now, image.SizeBytes); quotaErr != nil {
		return uuid.Nil, false, errors.WithStack(quotaErr)
	}

	imageID := gutils.UUID7Bytes()
	_, err = tx.ExecContext(ctx,
		rebindQuery(`INSERT INTO mcp_user_image_refs
			(id, user_identity, api_key_hash, sha256, storage_key, size_bytes, width, height, mime_type, original_mime, source_url, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, s.useDollar),
		imageID.String(),
		auth.UserIdentity,
		auth.APIKeyHash,
		image.SHA256,
		image.StorageKey,
		image.SizeBytes,
		image.Width,
		image.Height,
		defaultStoredImageContentType,
		image.OriginalMIME,
		nullableString(image.SourceURL),
		now,
		expiresAt,
	)
	if err != nil {
		return uuid.Nil, false, errors.Wrap(err, "insert image ref")
	}
	return imageID, false, nil
}

// checkImageQuota returns ErrQuotaExceeded when adding size bytes would push
// the user's live usage — live refs plus the live upload claims of requests
// still uploading — above the configured quota. The caller holds the scope lock.
func (s *Service) checkImageQuota(ctx context.Context, tx *sql.Tx, auth *askuser.AuthorizationContext, now time.Time, size int64) error {
	var refBytes, claimedBytes int64
	if err := tx.QueryRowContext(ctx,
		rebindQuery(
			`SELECT COALESCE(SUM(size_bytes), 0) FROM mcp_user_image_refs
			  WHERE user_identity = ? AND expires_at > ?`,
			s.useDollar,
		),
		auth.UserIdentity, now,
	).Scan(&refBytes); err != nil {
		return errors.Wrap(err, "reserve image usage")
	}
	if err := tx.QueryRowContext(ctx,
		rebindQuery(
			`SELECT COALESCE(SUM(size_bytes), 0) FROM mcp_user_image_claims
			  WHERE user_identity = ? AND kind = ? AND expires_at > ?`,
			s.useDollar,
		),
		auth.UserIdentity, claimUpload, now,
	).Scan(&claimedBytes); err != nil {
		return errors.Wrap(err, "reserve image claimed usage")
	}
	usedBytes := refBytes + claimedBytes

	quota := s.settings.Images.PerUserQuotaBytes
	if quota <= 0 {
		quota = DefaultImagePerUserQuotaBytes
	}
	if usedBytes+size > quota {
		return errors.WithStack(ErrQuotaExceeded)
	}
	return nil
}

// nullableString returns sql-friendly nil for the empty string.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CreateRequestWithImages creates a request with its normalized images as a
// reservation workflow in which no database transaction or connection is
// held across object-store I/O:
//
//  1. Reserve (short transaction, image scope lock): refuse keys whose delete
//     is in flight, check the quota and record upload claims, which count
//     toward the quota and protect the keys from deletion.
//  2. Upload every object outside any transaction, each with its own timeout.
//  3. Finalize (short transaction, scope lock): consume the claims, insert
//     the request, the image refs and links, and commit.
//
// If anything fails after the reservation — an upload (including one that
// failed after the server stored it), the finalize transaction, a canceled
// context, or a panic — the claims are released and every attempted object
// that nothing else references or claims is deleted under a delete lease
// (see releaseImageUploads). Objects shared with committed requests are kept.
//
// The images slice may be empty; in that case objects may be nil and behavior
// is identical to CreateRequest (the pure-text byte-for-byte compatibility
// guarantee still holds).
func (s *Service) CreateRequestWithImages(
	ctx context.Context,
	auth *askuser.AuthorizationContext,
	content string,
	taskID string,
	images []UploadedImage,
	objects ImageObjectWriter,
) (*Request, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}
	if s.settings.Images.MaxPerRequest > 0 && len(images) > s.settings.Images.MaxPerRequest {
		return nil, errors.WithStack(ErrTooManyImages)
	}
	if len(images) > 0 && objects == nil {
		return nil, errors.Wrap(ErrStorageUnavailable, "image object writer unavailable")
	}
	body, err := sanitizeImageRequestContent(content, len(images) > 0)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	taskID = normalizeTaskID(taskID)
	requestID := gutils.UUID7Bytes()

	if len(images) == 0 {
		return s.finalizeImageRequest(ctx, auth, requestID, body, taskID, nil)
	}

	if err := s.reserveImageUploads(ctx, auth, requestID, images); err != nil {
		return nil, errors.WithStack(err)
	}
	finalized := false
	var attempted []string // keys whose upload started, appended before each Put
	defer func() {
		if !finalized {
			s.releaseImageUploads(ctx, objects, auth, requestID, images, attempted)
		}
	}()

	if err := putImageObjects(ctx, objects, auth, images, &attempted); err != nil {
		return nil, errors.WithStack(err)
	}
	req, err := s.finalizeImageRequest(ctx, auth, requestID, body, taskID, images)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	finalized = true
	return req, nil
}

// finalizeImageRequest inserts the request with its image refs and links in
// one short transaction. With images it holds the scope lock and consumes the
// request's upload claims first, so the quota accounting moves from the
// claims to the refs atomically.
func (s *Service) finalizeImageRequest(
	ctx context.Context,
	auth *askuser.AuthorizationContext,
	requestID uuid.UUID,
	body string,
	taskID string,
	images []UploadedImage,
) (*Request, error) {
	tx, err := s.beginImageTx(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "begin create-with-images tx")
	}
	defer func() { _ = tx.Rollback() }()

	now := s.clock()
	if len(images) > 0 {
		keys := distinctImageKeys(images)
		if err := s.lockImageScopes(ctx, tx, keys); err != nil {
			return nil, errors.WithStack(err)
		}
		if err := s.consumeUploadClaims(ctx, tx, requestID, len(keys), now); err != nil {
			return nil, errors.WithStack(err)
		}
	}

	var maxOrder int
	if err := tx.QueryRowContext(ctx,
		rebindQuery(
			`SELECT COALESCE(MAX(sort_order), -1)
			   FROM mcp_user_requests
			  WHERE api_key_hash = ? AND status = ?`,
			s.useDollar,
		),
		auth.APIKeyHash, StatusPending,
	).Scan(&maxOrder); err != nil {
		return nil, errors.Wrap(err, "query max sort order")
	}

	req := &Request{
		ID:           requestID,
		Content:      body,
		Status:       StatusPending,
		TaskID:       taskID,
		SortOrder:    maxOrder + 1,
		APIKeyHash:   auth.APIKeyHash,
		KeySuffix:    auth.KeySuffix,
		UserIdentity: auth.UserIdentity,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if _, err := tx.ExecContext(ctx,
		rebindQuery(`INSERT INTO mcp_user_requests
			(id, content, status, task_id, sort_order, api_key_hash, key_suffix, user_identity, consumed_at, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, s.useDollar),
		req.ID.String(),
		req.Content,
		req.Status,
		req.TaskID,
		req.SortOrder,
		req.APIKeyHash,
		req.KeySuffix,
		req.UserIdentity,
		nil,
		req.CreatedAt,
		req.UpdatedAt,
	); err != nil {
		return nil, errors.Wrap(err, "insert user request")
	}

	linked, err := s.linkRequestImages(ctx, tx, auth, req, images, now)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if err := tx.Commit(); err != nil {
		return nil, errors.Wrap(err, "commit create-with-images")
	}
	req.Images = linked

	s.log().Info("user request created",
		zap.String("request_id", req.ID.String()),
		zap.String("user", auth.UserIdentity),
		zap.String("task_id", req.TaskID),
		zap.Int("image_count", len(linked)),
	)
	return req, nil
}

// linkRequestImages upserts the ref of every image (charging quota for new or
// revived refs) and links it to req inside tx.
func (s *Service) linkRequestImages(
	ctx context.Context,
	tx *sql.Tx,
	auth *askuser.AuthorizationContext,
	req *Request,
	images []UploadedImage,
	now time.Time,
) ([]RequestImage, error) {
	ttl := s.settings.Images.ObjectTTLDays
	if ttl <= 0 {
		ttl = DefaultImageObjectTTLDays
	}
	linked := make([]RequestImage, 0, len(images))
	for idx, img := range images {
		imageID, _, reserveErr := s.reserveImage(ctx, tx, auth, img)
		if reserveErr != nil {
			return nil, errors.WithStack(reserveErr)
		}
		if _, err := tx.ExecContext(ctx,
			rebindQuery(`INSERT INTO mcp_user_request_image_links (request_id, image_id, sort_order) VALUES (?, ?, ?)`, s.useDollar),
			req.ID.String(), imageID.String(), idx,
		); err != nil {
			return nil, errors.Wrap(err, "link image to request")
		}
		linked = append(linked, RequestImage{
			ID:           imageID,
			RequestID:    req.ID,
			UserIdentity: auth.UserIdentity,
			APIKeyHash:   auth.APIKeyHash,
			StorageKey:   img.StorageKey,
			SHA256:       img.SHA256,
			SizeBytes:    img.SizeBytes,
			Width:        img.Width,
			Height:       img.Height,
			MIMEType:     defaultStoredImageContentType,
			OriginalMIME: img.OriginalMIME,
			SourceURL:    img.SourceURL,
			SortOrder:    idx,
			CreatedAt:    now,
			ExpiresAt:    now.Add(time.Duration(ttl) * 24 * time.Hour),
		})
	}
	return linked, nil
}

// sanitizeImageRequestContent validates request text. Content may be empty
// when at least one image is attached (image-only messages).
func sanitizeImageRequestContent(content string, hasImages bool) (string, error) {
	if hasImages && strings.TrimSpace(content) == "" {
		return "", nil
	}
	body, err := sanitizeRequestContent(content)
	if err != nil {
		return "", errors.WithStack(err)
	}
	return body, nil
}

// putImageObjects uploads every image in order, each bounded by
// imageObjectPutTimeout, outside any transaction. Each key is appended to
// attempted before its Put starts, because a Put that reports failure (for
// example a client timeout) may still have been stored by the server.
func putImageObjects(ctx context.Context, objects ImageObjectWriter, auth *askuser.AuthorizationContext, images []UploadedImage, attempted *[]string) error {
	for idx, img := range images {
		*attempted = append(*attempted, img.StorageKey)
		putCtx, cancel := context.WithTimeout(ctx, imageObjectPutTimeout)
		err := objects.PutImage(putCtx, auth, img)
		cancel()
		if err != nil {
			return errors.Wrapf(err, "attachment index %d", idx)
		}
	}
	return nil
}

// loadImagesForRequests populates the Images field of every request in the
// provided slice using a single JOIN query.
func (s *Service) loadImagesForRequests(ctx context.Context, requests []Request) error {
	if len(requests) == 0 {
		return nil
	}
	byID := make(map[string]int, len(requests))
	placeholders := make([]string, 0, len(requests))
	args := make([]any, 0, len(requests)+1)
	now := s.clock()
	for i, r := range requests {
		byID[r.ID.String()] = i
		placeholders = append(placeholders, "?")
		args = append(args, r.ID.String())
	}
	args = append(args, now)

	query := fmt.Sprintf(
		`SELECT l.request_id, l.sort_order, i.id, i.user_identity, i.api_key_hash, i.storage_key, i.sha256, i.size_bytes, i.width, i.height, i.mime_type, i.original_mime, COALESCE(i.source_url, ''), i.created_at, i.expires_at
		   FROM mcp_user_request_image_links l
		   JOIN mcp_user_image_refs i ON i.id = l.image_id
		  WHERE l.request_id IN (%s) AND i.expires_at > ?
		  ORDER BY l.request_id, l.sort_order`,
		strings.Join(placeholders, ","),
	)
	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return errors.Wrap(err, "load request images")
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			reqIDStr   string
			sortOrder  int
			imgIDStr   string
			userID     string
			keyHash    string
			storageKey string
			sha        string
			size       int64
			w, h       int
			mime       string
			origMime   string
			sourceURL  string
			createdRaw any
			expiresRaw any
		)
		if err := rows.Scan(&reqIDStr, &sortOrder, &imgIDStr, &userID, &keyHash, &storageKey, &sha, &size, &w, &h, &mime, &origMime, &sourceURL, &createdRaw, &expiresRaw); err != nil {
			return errors.Wrap(err, "scan image join")
		}
		idx, ok := byID[reqIDStr]
		if !ok {
			continue
		}
		imgID, err := uuid.Parse(imgIDStr)
		if err != nil {
			return errors.Wrap(err, "parse image id")
		}
		reqID, err := uuid.Parse(reqIDStr)
		if err != nil {
			return errors.Wrap(err, "parse request id")
		}
		createdAt, err := parseSQLTime(createdRaw)
		if err != nil {
			return errors.Wrap(err, "parse image created_at")
		}
		expiresAt, err := parseSQLTime(expiresRaw)
		if err != nil {
			return errors.Wrap(err, "parse image expires_at")
		}
		requests[idx].Images = append(requests[idx].Images, RequestImage{
			ID:           imgID,
			RequestID:    reqID,
			UserIdentity: userID,
			APIKeyHash:   keyHash,
			StorageKey:   storageKey,
			SHA256:       sha,
			SizeBytes:    size,
			Width:        w,
			Height:       h,
			MIMEType:     mime,
			OriginalMIME: origMime,
			SourceURL:    sourceURL,
			SortOrder:    sortOrder,
			CreatedAt:    createdAt,
			ExpiresAt:    expiresAt,
		})
	}
	if err := rows.Err(); err != nil {
		return errors.Wrap(err, "iterate image rows")
	}
	return nil
}

// GCExpiredImageRefs deletes expired image reference rows and then the link
// rows that pointed at them. The ref delete re-checks expires_at, so a row a
// concurrent request revives (refreshes) after the candidate scan is kept, as
// are its links. Objects are not deleted here: an orphan claim is recorded for
// each candidate key so the reconciler deletes the object unless something
// references or claims it again (the bucket lifecycle rule is the backstop).
// Claims that expired more than one object TTL ago are purged, which bounds
// the claims table when the reconciler is disabled.
func (s *Service) GCExpiredImageRefs(ctx context.Context) (int64, error) {
	now := s.clock()
	expired, err := s.expiredImageRefs(ctx, now)
	if err != nil {
		return 0, errors.WithStack(err)
	}
	expiredIDs := make([]string, 0, len(expired))
	for _, ref := range expired {
		expiredIDs = append(expiredIDs, ref.id)
	}
	deleted, err := s.deleteExpiredImageRefs(ctx, expiredIDs, now)
	if err != nil {
		return 0, errors.WithStack(err)
	}
	for _, ref := range expired {
		if _, err := s.execContext(ctx,
			`INSERT INTO mcp_user_image_claims (id, owner_id, user_identity, storage_key, kind, size_bytes, created_at, expires_at)
				VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
			gutils.UUID7Bytes().String(), gutils.UUID7Bytes().String(), ref.userIdentity, ref.storageKey, claimOrphan, now, now,
		); err != nil {
			return deleted, errors.Wrap(err, "record orphan image claim")
		}
	}
	ttl := s.settings.Images.ObjectTTLDays
	if ttl <= 0 {
		ttl = DefaultImageObjectTTLDays
	}
	if _, err := s.execContext(ctx,
		`DELETE FROM mcp_user_image_claims WHERE expires_at <= ?`,
		now.Add(-time.Duration(ttl)*24*time.Hour),
	); err != nil {
		return deleted, errors.Wrap(err, "purge stale image claims")
	}
	return deleted, nil
}

// expiredImageRef is a candidate of GCExpiredImageRefs.
type expiredImageRef struct {
	id           string
	userIdentity string
	storageKey   string
}

// expiredImageRefs lists the refs that expired at or before now.
func (s *Service) expiredImageRefs(ctx context.Context, now time.Time) ([]expiredImageRef, error) {
	rows, err := s.queryContext(ctx,
		`SELECT id, user_identity, storage_key FROM mcp_user_image_refs WHERE expires_at <= ?`,
		now,
	)
	if err != nil {
		return nil, errors.Wrap(err, "select expired images")
	}
	defer func() { _ = rows.Close() }()
	expired := make([]expiredImageRef, 0)
	for rows.Next() {
		var ref expiredImageRef
		if err := rows.Scan(&ref.id, &ref.userIdentity, &ref.storageKey); err != nil {
			return nil, errors.Wrap(err, "scan expired image id")
		}
		expired = append(expired, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate expired image rows")
	}
	return expired, nil
}

// deleteExpiredImageRefs deletes the candidate refs that are still expired at
// now, then the links of the refs that are gone. The expiry re-check runs in
// the DELETE itself, so on PostgreSQL a row a concurrent request is refreshing
// is re-evaluated after its row lock is released and kept.
func (s *Service) deleteExpiredImageRefs(ctx context.Context, expiredIDs []string, now time.Time) (int64, error) {
	if len(expiredIDs) == 0 {
		return 0, nil
	}
	placeholders := make([]string, 0, len(expiredIDs))
	args := make([]any, 0, len(expiredIDs)+1)
	for _, id := range expiredIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	in := strings.Join(placeholders, ",")
	refSQL := fmt.Sprintf(`DELETE FROM mcp_user_image_refs WHERE id IN (%s) AND expires_at <= ?`, in)
	res, err := s.execContext(ctx, refSQL, append(args, now)...)
	if err != nil {
		return 0, errors.Wrap(err, "delete expired images")
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, errors.Wrap(err, "read expired images rows affected")
	}
	linkSQL := fmt.Sprintf(`DELETE FROM mcp_user_request_image_links
		WHERE image_id IN (%s)
		  AND NOT EXISTS (SELECT 1 FROM mcp_user_image_refs r WHERE r.id = mcp_user_request_image_links.image_id)`, in)
	if _, err := s.execContext(ctx, linkSQL, args...); err != nil {
		return 0, errors.Wrap(err, "delete expired image links")
	}
	return affected, nil
}

// StartImageGCWorker runs GCExpiredImageRefs on a fixed interval until ctx is canceled.
func (s *Service) StartImageGCWorker(ctx context.Context) {
	if s == nil || s.settings.Images.GCSweepInterval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(s.settings.Images.GCSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute) //nolint:contextcheck // detached context for background sweep
				if _, err := s.GCExpiredImageRefs(sweepCtx); err != nil {
					s.log().Warn("gc expired image refs", zap.Error(err))
				}
				cancel()
			}
		}
	}()
}
