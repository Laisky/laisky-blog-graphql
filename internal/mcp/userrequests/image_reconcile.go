package userrequests

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"sort"
	"time"

	errors "github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/Laisky/zap"
)

// imageReconcileTimeout bounds one reconciler pass.
const imageReconcileTimeout = 5 * time.Minute

// ImageReconcileResult summarizes one ReconcileImageObjects pass.
type ImageReconcileResult struct {
	// Claims is how many due claims the pass processed.
	Claims int
	// Keys is how many distinct storage keys those claims named.
	Keys int
	// Deleted is how many objects were deleted because nothing referenced or
	// claimed them any more.
	Deleted int
}

// dueImageClaim is a claim whose deadline passed (or an orphan claim).
type dueImageClaim struct {
	id           string
	key          string
	userIdentity string
}

// ReconcileImageObjects is one bounded pass of the orphan-object reconciler.
// It works only from this database's claims, never from a bucket listing, so
// it cannot touch objects another deployment or application wrote:
//
//   - expired upload claims (a create that crashed between reserving and
//     finalizing, or whose release failed),
//   - expired delete leases (a delete that failed or timed out),
//   - orphan claims (refs the GC expired).
//
// For each named key it releases the due claims and, under the image scope
// lock, leases the key unless an image ref or a live claim protects it, then
// deletes the object outside any transaction. A key whose delete fails keeps
// its lease and is retried once that expires. At most OrphanSweepBatch claims
// are processed per pass.
func (s *Service) ReconcileImageObjects(ctx context.Context, objects ImageObjectWriter) (ImageReconcileResult, error) {
	var result ImageReconcileResult
	if objects == nil {
		return result, errors.Wrap(ErrStorageUnavailable, "image object writer unavailable")
	}
	batch := s.settings.Images.OrphanSweepBatch
	if batch <= 0 {
		batch = DefaultImageOrphanSweepBatch
	}
	due, err := s.dueImageClaims(ctx, batch)
	if err != nil {
		return result, errors.WithStack(err)
	}
	result.Claims = len(due)
	byKey := make(map[string][]dueImageClaim)
	for _, claim := range due {
		byKey[claim.key] = append(byKey[claim.key], claim)
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if di, dj := path.Dir(keys[i]), path.Dir(keys[j]); di != dj {
			return di < dj
		}
		return keys[i] < keys[j]
	})
	result.Keys = len(keys)

	var failed []error
	for _, key := range keys {
		deleted, err := s.reconcileImageKey(ctx, objects, key, byKey[key])
		if deleted {
			result.Deleted++
		}
		if err != nil {
			failed = append(failed, errors.Wrapf(err, "reconcile %q", key))
		}
	}
	if len(failed) > 0 {
		return result, errors.Join(failed...)
	}
	return result, nil
}

// dueImageClaims lists up to limit claims whose deadline has passed (orphan
// claims are due when created), oldest first.
func (s *Service) dueImageClaims(ctx context.Context, limit int) ([]dueImageClaim, error) {
	rows, err := s.queryContext(ctx,
		fmt.Sprintf(`SELECT id, storage_key, user_identity FROM mcp_user_image_claims
			WHERE expires_at <= ? ORDER BY expires_at LIMIT %d`, limit),
		s.clock(),
	)
	if err != nil {
		return nil, errors.Wrap(err, "list due image claims")
	}
	defer func() { _ = rows.Close() }()
	var out []dueImageClaim
	for rows.Next() {
		var c dueImageClaim
		if err := rows.Scan(&c.id, &c.key, &c.userIdentity); err != nil {
			return nil, errors.Wrap(err, "scan due image claim")
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate due image claims")
	}
	return out, nil
}

// reconcileImageKey releases the due claims of key and deletes its object if
// nothing protects it. It reports whether the object was deleted.
func (s *Service) reconcileImageKey(ctx context.Context, objects ImageObjectWriter, key string, claims []dueImageClaim) (bool, error) {
	run := gutils.UUID7Bytes()
	ids := make([]string, len(claims))
	for i, c := range claims {
		ids[i] = c.id
	}
	leased, err := s.leaseUnprotectedImageKeys(ctx, run, claims[0].userIdentity, []string{key}, []string{key},
		func(ctx context.Context, tx *sql.Tx, now time.Time) error {
			in, args := inPlaceholders(ids)
			// Only claims that are still due: one refreshed meanwhile is kept.
			if _, err := tx.ExecContext(ctx,
				rebindQuery(fmt.Sprintf(`DELETE FROM mcp_user_image_claims WHERE id IN (%s) AND expires_at <= ?`, in), s.useDollar),
				append(args, now)...,
			); err != nil {
				return errors.Wrap(err, "release due image claims")
			}
			return nil
		})
	if err != nil {
		return false, errors.WithStack(err)
	}
	deleted, err := s.deleteLeasedImageObjects(ctx, objects, run, leased)
	return len(deleted) > 0, errors.WithStack(err)
}

// StartImageReconcileWorker runs ReconcileImageObjects every
// OrphanSweepInterval until ctx is canceled.
func (s *Service) StartImageReconcileWorker(ctx context.Context, objects ImageObjectWriter) {
	if s == nil || objects == nil || s.settings.Images.OrphanSweepInterval <= 0 {
		return
	}
	logger := s.log().Named("image_reconciler")
	go func() {
		ticker := time.NewTicker(s.settings.Images.OrphanSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				passCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), imageReconcileTimeout) //nolint:contextcheck // detached context for background sweep
				result, err := s.ReconcileImageObjects(passCtx, objects)
				cancel()
				if err != nil {
					logger.Error("reconcile image objects", zap.Error(err),
						zap.Int("claims", result.Claims), zap.Int("deleted", result.Deleted))
					continue
				}
				if result.Deleted > 0 {
					logger.Info("deleted orphan image objects",
						zap.Int("claims", result.Claims), zap.Int("deleted", result.Deleted))
				}
			}
		}
	}()
}
