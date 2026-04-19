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

	for _, stmt := range statements {
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

// reserveImage performs the upsert-with-quota dance inside a single transaction.
// If the SHA already exists for this user it refreshes the row's TTL and returns
// the existing row id with existed=true. Otherwise it inserts a new row after
// verifying the user is still under quota.
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
		_ = existingExpiresRaw
		id, parseErr := uuid.Parse(existingID)
		if parseErr != nil {
			return uuid.Nil, false, errors.Wrap(parseErr, "parse existing image id")
		}
		// Refresh TTL (do not re-charge quota).
		if _, execErr := tx.ExecContext(ctx,
			rebindQuery(`UPDATE mcp_user_image_refs SET expires_at = ? WHERE id = ?`, s.useDollar),
			expiresAt, existingID,
		); execErr != nil {
			return uuid.Nil, false, errors.Wrap(execErr, "refresh image ttl")
		}
		return id, true, nil
	}

	var usedBytes int64
	if err := tx.QueryRowContext(ctx,
		rebindQuery(
			`SELECT COALESCE(SUM(size_bytes), 0) FROM mcp_user_image_refs
			  WHERE user_identity = ? AND expires_at > ?`,
			s.useDollar,
		),
		auth.UserIdentity, now,
	).Scan(&usedBytes); err != nil {
		return uuid.Nil, false, errors.Wrap(err, "reserve image usage")
	}

	quota := s.settings.Images.PerUserQuotaBytes
	if quota <= 0 {
		quota = DefaultImagePerUserQuotaBytes
	}
	if usedBytes+image.SizeBytes > quota {
		return uuid.Nil, false, errors.WithStack(ErrQuotaExceeded)
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
		"image/png",
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

// nullableString returns sql-friendly nil for the empty string.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CreateRequestWithImages creates a request and links the already-normalized
// images to it atomically. The images slice may be empty; in that case behavior
// is identical to CreateRequest (and the pure-text byte-for-byte compatibility
// guarantee still holds).
func (s *Service) CreateRequestWithImages(
	ctx context.Context,
	auth *askuser.AuthorizationContext,
	content string,
	taskID string,
	images []UploadedImage,
) (*Request, error) {
	if auth == nil {
		return nil, ErrInvalidAuthorization
	}
	if s.settings.Images.MaxPerRequest > 0 && len(images) > s.settings.Images.MaxPerRequest {
		return nil, errors.WithStack(ErrTooManyImages)
	}

	// Content may be empty if at least one image is attached (image-only messages).
	var body string
	if len(images) == 0 {
		var err error
		body, err = sanitizeRequestContent(content)
		if err != nil {
			return nil, errors.WithStack(err)
		}
	} else {
		trimmed := strings.TrimSpace(content)
		if trimmed != "" {
			var err error
			body, err = sanitizeRequestContent(content)
			if err != nil {
				return nil, errors.WithStack(err)
			}
		}
	}

	taskID = normalizeTaskID(taskID)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, errors.Wrap(err, "begin create-with-images tx")
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

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

	now := s.clock()
	req := &Request{
		ID:           gutils.UUID7Bytes(),
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

	linked := make([]RequestImage, 0, len(images))
	for idx, img := range images {
		imageID, existed, reserveErr := s.reserveImage(ctx, tx, auth, img)
		if reserveErr != nil {
			return nil, errors.WithStack(reserveErr)
		}
		_ = existed
		if _, err := tx.ExecContext(ctx,
			rebindQuery(`INSERT INTO mcp_user_request_image_links (request_id, image_id, sort_order) VALUES (?, ?, ?)`, s.useDollar),
			req.ID.String(), imageID.String(), idx,
		); err != nil {
			return nil, errors.Wrap(err, "link image to request")
		}

		ttl := s.settings.Images.ObjectTTLDays
		if ttl <= 0 {
			ttl = DefaultImageObjectTTLDays
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
			MIMEType:     "image/png",
			OriginalMIME: img.OriginalMIME,
			SourceURL:    img.SourceURL,
			SortOrder:    idx,
			CreatedAt:    now,
			ExpiresAt:    now.Add(time.Duration(ttl) * 24 * time.Hour),
		})
	}

	if err := tx.Commit(); err != nil {
		return nil, errors.Wrap(err, "commit create-with-images")
	}
	committed = true
	req.Images = linked

	s.log().Info("user request created",
		zap.String("request_id", req.ID.String()),
		zap.String("user", auth.UserIdentity),
		zap.String("task_id", req.TaskID),
		zap.Int("image_count", len(linked)),
	)
	return req, nil
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

// GCExpiredImageRefs deletes expired image reference rows (and the matching
// link rows cascade via the request_image_links table). MinIO objects are
// expired by bucket lifecycle rule, so the server only owns DB cleanup.
func (s *Service) GCExpiredImageRefs(ctx context.Context) (int64, error) {
	now := s.clock()
	// First gather expired ids so link rows can be removed first (avoids FK issues on DBs without cascade).
	rows, err := s.queryContext(ctx,
		`SELECT id FROM mcp_user_image_refs WHERE expires_at <= ?`,
		now,
	)
	if err != nil {
		return 0, errors.Wrap(err, "select expired images")
	}
	expiredIDs := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, errors.Wrap(err, "scan expired image id")
		}
		expiredIDs = append(expiredIDs, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, errors.Wrap(err, "iterate expired image rows")
	}
	_ = rows.Close()
	if len(expiredIDs) == 0 {
		return 0, nil
	}

	placeholders := make([]string, 0, len(expiredIDs))
	args := make([]any, 0, len(expiredIDs))
	for _, id := range expiredIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	linkSQL := fmt.Sprintf(`DELETE FROM mcp_user_request_image_links WHERE image_id IN (%s)`, strings.Join(placeholders, ","))
	if _, err := s.execContext(ctx, linkSQL, args...); err != nil {
		return 0, errors.Wrap(err, "delete expired image links")
	}
	refSQL := fmt.Sprintf(`DELETE FROM mcp_user_image_refs WHERE id IN (%s)`, strings.Join(placeholders, ","))
	res, err := s.execContext(ctx, refSQL, args...)
	if err != nil {
		return 0, errors.Wrap(err, "delete expired images")
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, errors.Wrap(err, "read expired images rows affected")
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
