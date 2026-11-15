package userrequests

import (
	"context"
	"database/sql"
	"time"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
)

// Image quota accounting (proposal §3.5). A user's usage is the size of the
// distinct images (by sha256) the user holds: an image counts once when a live
// ref of the user names it, or else once while any live upload claim of the
// user reserves it, however many requests reserve it at the same time.
// Admission (reserveImageUploads, finalizeImageRequest) and the report
// (QuotaUsage, GET /api/quota) both use imageUsage, so the report's
// available_bytes is exactly what the next reservation may add.
//
// Every admission runs under the user's quota lock (imageQuotaLockID), which
// depends only on the user identity, never on the key prefix: instances with
// different prefixes still serialize one user's admissions. An admission
// writes its change first and then verifies the resulting usage
// (verifyImageQuota) inside the same transaction, so the check always sees
// what it is admitting, including the de-duplication against other requests.

// imageUsage is one user's image storage accounting at one instant.
type imageUsage struct {
	// committedBytes is the size of the user's live refs.
	committedBytes int64
	// objectCount is how many live refs the user holds.
	objectCount int64
	// reservedBytes is the size of the images the user's live upload claims
	// reserve and no live ref of the user holds yet, each image once.
	reservedBytes int64
}

// total is the usage the quota bounds.
func (u imageUsage) total() int64 {
	return u.committedBytes + u.reservedBytes
}

// sqlRowQueryer is satisfied by both *sql.DB and *sql.Tx.
type sqlRowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// imageUsage computes userIdentity's usage at now through q (the database or
// an admission transaction holding the user's quota lock).
//
// Upload claims written by earlier builds of this release carry no sha256;
// they are charged by key (each once) with the size they recorded.
func (s *Service) imageUsage(ctx context.Context, q sqlRowQueryer, userIdentity string, now time.Time) (imageUsage, error) {
	var usage imageUsage
	if err := q.QueryRowContext(ctx,
		rebindQuery(`SELECT COALESCE(SUM(size_bytes), 0), COUNT(1) FROM mcp_user_image_refs
			WHERE user_identity = ? AND expires_at > ?`, s.useDollar),
		userIdentity, now,
	).Scan(&usage.committedBytes, &usage.objectCount); err != nil {
		return usage, errors.Wrap(err, "query committed image usage")
	}
	if err := q.QueryRowContext(ctx,
		rebindQuery(`SELECT COALESCE(SUM(reserved.size_bytes), 0) FROM (
			SELECT MAX(c.size_bytes) AS size_bytes
			  FROM mcp_user_image_claims c
			 WHERE c.user_identity = ? AND c.kind = ? AND c.expires_at > ?
			   AND NOT EXISTS (
			       SELECT 1 FROM mcp_user_image_refs r
			        WHERE r.user_identity = c.user_identity AND r.sha256 = c.sha256 AND r.expires_at > ?)
			 GROUP BY COALESCE(c.sha256, c.storage_key)
		) reserved`, s.useDollar),
		userIdentity, claimUpload, now, now,
	).Scan(&usage.reservedBytes); err != nil {
		return usage, errors.Wrap(err, "query reserved image usage")
	}
	return usage, nil
}

// effectiveImageQuotaBytes is the per-user quota admission enforces: the
// configured one, or the default when it is unset (zero or negative).
func (s *Service) effectiveImageQuotaBytes() int64 {
	if quota := s.settings.Images.PerUserQuotaBytes; quota > 0 {
		return quota
	}
	return DefaultImagePerUserQuotaBytes
}

// effectiveImageTTLDays is how many days a ref lives after its last use: the
// configured object TTL, or the default when it is unset. (An unset TTL also
// disables the bucket lifecycle rule; refs still expire.)
func (s *Service) effectiveImageTTLDays() int {
	if ttl := s.settings.Images.ObjectTTLDays; ttl > 0 {
		return ttl
	}
	return DefaultImageObjectTTLDays
}

// verifyImageQuota returns ErrQuotaExceeded when userIdentity's usage, as
// tx sees it after the admission's own writes, is above the quota. The caller
// holds the user's quota lock.
func (s *Service) verifyImageQuota(ctx context.Context, tx *sql.Tx, userIdentity string, now time.Time) error {
	usage, err := s.imageUsage(ctx, tx, userIdentity, now)
	if err != nil {
		return errors.WithStack(err)
	}
	if usage.total() > s.effectiveImageQuotaBytes() {
		return errors.WithStack(ErrQuotaExceeded)
	}
	return nil
}

// QuotaUsage reports a user's image storage footprint with the accounting
// quota admission uses.
type QuotaUsage struct {
	UserIdentity string
	// UsedBytes is the size of the user's live (committed) image refs.
	UsedBytes int64
	// ReservedBytes is the size of the images requests of the user are still
	// uploading (live upload claims), each image once and only when no live
	// ref already holds it.
	ReservedBytes int64
	// AvailableBytes is how many new bytes the next reservation may add:
	// QuotaBytes - UsedBytes - ReservedBytes, at least 0.
	AvailableBytes int64
	// QuotaBytes is the enforced per-user quota.
	QuotaBytes int64
	// ObjectCount is how many live refs the user holds.
	ObjectCount int64
	// TTLDays is how many days an image lives after its last use.
	TTLDays int
}

// QuotaUsage returns the user's live image usage, its reservations, and what
// the quota still admits.
func (s *Service) QuotaUsage(ctx context.Context, auth *askuser.AuthorizationContext) (QuotaUsage, error) {
	if auth == nil {
		return QuotaUsage{}, ErrInvalidAuthorization
	}
	usage, err := s.imageUsage(ctx, s.db, auth.UserIdentity, s.clock())
	if err != nil {
		return QuotaUsage{}, errors.Wrap(err, "query quota usage")
	}
	quota := s.effectiveImageQuotaBytes()
	return QuotaUsage{
		UserIdentity:   auth.UserIdentity,
		UsedBytes:      usage.committedBytes,
		ReservedBytes:  usage.reservedBytes,
		AvailableBytes: max(quota-usage.total(), 0),
		QuotaBytes:     quota,
		ObjectCount:    usage.objectCount,
		TTLDays:        s.effectiveImageTTLDays(),
	}, nil
}

// ensureImageClaimSHAColumn adds mcp_user_image_claims.sha256 to a claims
// table an earlier build of this release created without it. The column is
// nullable, so existing rows are untouched.
func (s *Service) ensureImageClaimSHAColumn(ctx context.Context) error {
	if s.useDollar {
		if _, err := s.execContext(ctx, `ALTER TABLE mcp_user_image_claims ADD COLUMN IF NOT EXISTS sha256 CHAR(64) NULL`); err != nil {
			return errors.Wrap(err, "add image claim sha256 column")
		}
		return nil
	}
	found, err := s.sqliteImageClaimsHaveSHA(ctx)
	if err != nil || found {
		return errors.WithStack(err)
	}
	if _, err := s.execContext(ctx, `ALTER TABLE mcp_user_image_claims ADD COLUMN sha256 CHAR(64) NULL`); err != nil {
		return errors.Wrap(err, "add image claim sha256 column")
	}
	return nil
}

// sqliteImageClaimsHaveSHA reports whether the SQLite claims table already
// has the sha256 column.
func (s *Service) sqliteImageClaimsHaveSHA(ctx context.Context) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(mcp_user_image_claims)`)
	if err != nil {
		return false, errors.Wrap(err, "inspect image claims table")
	}
	defer func() { _ = rows.Close() }()
	found := false
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, errors.Wrap(err, "scan image claims column")
		}
		found = found || name == "sha256"
	}
	if err := rows.Err(); err != nil {
		return false, errors.Wrap(err, "iterate image claims columns")
	}
	return found, nil
}
