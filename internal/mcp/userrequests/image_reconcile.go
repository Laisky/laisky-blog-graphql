package userrequests

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"slices"
	"sort"
	"time"

	errors "github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/Laisky/zap"
	"github.com/google/uuid"
)

const (
	// imageReconcileTimeout bounds one reconciler pass.
	imageReconcileTimeout = 5 * time.Minute
	// imageReconcileMaxPasses bounds the passes of one reconciler tick. A
	// pass that processed a full batch is followed by another, so a backlog
	// drains at up to this many batches per interval instead of one.
	imageReconcileMaxPasses = 8
	// imageReconcileBacklogAlertAge is how long the oldest due claim may wait
	// before the reconciler reports a stuck backlog at ERROR.
	imageReconcileBacklogAlertAge = 24 * time.Hour
	// imageReconcileBacklogAlertEvery rate-limits that ERROR.
	imageReconcileBacklogAlertEvery = 6 * time.Hour
)

// ImageReconcileResult summarizes one ReconcileImageObjects pass, or the
// passes of one reconciler tick.
type ImageReconcileResult struct {
	// Claims is how many due claims the pass processed.
	Claims int
	// Keys is how many distinct storage keys those claims named.
	Keys int
	// Deleted is how many objects were deleted because nothing referenced or
	// claimed them any more.
	Deleted int
	// Passes is how many passes ran (1 for ReconcileImageObjects).
	Passes int
	// Resolved is how many of Keys were settled: deleted, or released
	// because a ref or a live claim protects them.
	Resolved int
}

// add accumulates another pass into r.
func (r *ImageReconcileResult) add(other ImageReconcileResult) {
	r.Claims += other.Claims
	r.Keys += other.Keys
	r.Deleted += other.Deleted
	r.Passes += other.Passes
	r.Resolved += other.Resolved
}

// ImageClaimBacklog is the due part of the claims table.
type ImageClaimBacklog struct {
	// Due is how many claims are due.
	Due int64
	// OldestDueAge is how long the oldest of them has been due (0 when none).
	OldestDueAge time.Duration
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
//   - due orphan claims (refs the GC expired or finalize re-pointed, and keys
//     a failed create started uploading, once the late-write window passed).
//
// For each named key it releases the due claims and, in one short transaction
// under the image scope lock, leases the key unless an image ref or a live
// claim protects it. The leased objects are then deleted outside any
// transaction, imageDeleteConcurrency keys at a time. A key whose delete
// fails keeps its lease and is retried once that expires. At most
// OrphanSweepBatch claims are processed per pass, and a pass whose context
// ends leaves the rest due for the next one.
func (s *Service) ReconcileImageObjects(ctx context.Context, objects ImageObjectWriter) (ImageReconcileResult, error) {
	result := ImageReconcileResult{Passes: 1}
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

	// Keys are leased and deleted in chunks of imageDeleteConcurrency, so a
	// key stays leased (and its re-upload refused) only while its own chunk
	// is deleted, not for the whole pass.
	run := gutils.UUID7Bytes()
	var failed []error
	for chunk := range slices.Chunk(keys, imageDeleteConcurrency) {
		if err := ctx.Err(); err != nil {
			failed = append(failed, errors.Wrap(err, "reconcile pass ended"))
			break
		}
		leased := make([]string, 0, len(chunk))
		for _, key := range chunk {
			got, err := s.leaseDueImageKey(ctx, run, key, byKey[key])
			if err != nil {
				failed = append(failed, errors.Wrapf(err, "reconcile %q", key))
				continue
			}
			if len(got) == 0 {
				result.Resolved++ // protected: its claims were released, nothing to delete
			}
			leased = append(leased, got...)
		}
		deleted, err := s.deleteLeasedImageObjects(ctx, objects, run, leased, deletePathReconciler)
		result.Deleted += len(deleted)
		result.Resolved += len(deleted)
		if err != nil {
			failed = append(failed, errors.WithStack(err))
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

// leaseDueImageKey releases the due claims of key and leases it for deletion
// under run if nothing protects it. It returns the leased key, if any.
func (s *Service) leaseDueImageKey(ctx context.Context, run uuid.UUID, key string, claims []dueImageClaim) ([]string, error) {
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
		return nil, errors.WithStack(err)
	}
	return leased, nil
}

// ReconcileImageObjectsTick is one reconciler tick: passes of
// ReconcileImageObjects until one processes less than a full batch, ctx ends
// (checked between passes), a pass settles no key at all, or
// imageReconcileMaxPasses ran. Each pass runs detached from ctx with its own
// imageReconcileTimeout, so shutdown never interrupts a pass midway. A pass
// with failures does not end the tick by itself: a key whose delete failed
// keeps a live lease, so the next pass works on other claims, and one key
// that keeps failing cannot hold the backlog to one batch per interval. A
// pass that settles nothing (the store or the database is down) ends it, so
// failures are not retried in a hot loop. It records the pass metrics and,
// at the end, the remaining backlog; errors of every pass are joined.
func (s *Service) ReconcileImageObjectsTick(ctx context.Context, objects ImageObjectWriter) (ImageReconcileResult, ImageClaimBacklog, error) {
	batch := s.settings.Images.OrphanSweepBatch
	if batch <= 0 {
		batch = DefaultImageOrphanSweepBatch
	}
	detached := context.WithoutCancel(ctx) //nolint:contextcheck // a started pass finishes; ctx only stops further passes
	var total ImageReconcileResult
	var tickErr error
	for range imageReconcileMaxPasses {
		if ctx.Err() != nil {
			break
		}
		passCtx, cancel := context.WithTimeout(detached, imageReconcileTimeout)
		result, err := s.ReconcileImageObjects(passCtx, objects)
		cancel()
		total.add(result)
		if err != nil {
			imageMetrics.reconcilePasses.WithLabelValues("error").Inc()
			tickErr = errors.Join(tickErr, errors.WithStack(err))
			if result.Resolved == 0 {
				break
			}
		} else {
			imageMetrics.reconcilePasses.WithLabelValues("ok").Inc()
			imageMetrics.lastReconcileSuccess.Set(float64(s.clock().Unix()))
		}
		if result.Claims < batch {
			break
		}
	}
	backlogCtx, cancel := context.WithTimeout(detached, imageReconcileTimeout)
	defer cancel()
	backlog, err := s.imageClaimBacklog(backlogCtx)
	if err != nil {
		return total, backlog, errors.Join(tickErr, errors.WithStack(err))
	}
	imageMetrics.dueClaims.Set(float64(backlog.Due))
	imageMetrics.oldestDueClaimAge.Set(backlog.OldestDueAge.Seconds())
	return total, backlog, tickErr
}

// imageClaimBacklog counts the due claims and the age of the oldest one.
func (s *Service) imageClaimBacklog(ctx context.Context) (ImageClaimBacklog, error) {
	now := s.clock()
	var (
		backlog ImageClaimBacklog
		oldest  any
	)
	if err := s.queryRowContext(ctx,
		`SELECT COUNT(1), MIN(expires_at) FROM mcp_user_image_claims WHERE expires_at <= ?`, now,
	).Scan(&backlog.Due, &oldest); err != nil {
		return backlog, errors.Wrap(err, "count due image claims")
	}
	if oldest == nil {
		return backlog, nil
	}
	at, err := parseSQLTime(oldest)
	if err != nil {
		return backlog, errors.Wrap(err, "parse oldest due image claim")
	}
	backlog.OldestDueAge = max(now.Sub(at), 0)
	return backlog, nil
}

// StartImageReconcileWorker runs ReconcileImageObjectsTick every
// OrphanSweepInterval until ctx is canceled. A tick that fails is logged at
// ERROR; a backlog whose oldest claim has been due for longer than
// imageReconcileBacklogAlertAge is reported at ERROR at most every
// imageReconcileBacklogAlertEvery, because objects are then not being
// cleaned up (see the mcp_user_image_* metrics).
func (s *Service) StartImageReconcileWorker(ctx context.Context, objects ImageObjectWriter) {
	if s == nil || objects == nil || s.settings.Images.OrphanSweepInterval <= 0 {
		return
	}
	registerImageMetrics()
	imageMetrics.reconcilerEnabled.Set(1)
	logger := s.log().Named("image_reconciler")
	go func() {
		ticker := time.NewTicker(s.settings.Images.OrphanSweepInterval)
		defer ticker.Stop()
		var lastBacklogAlert time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				result, backlog, err := s.ReconcileImageObjectsTick(ctx, objects)
				if err != nil {
					logger.Error("reconcile image objects", zap.Error(err),
						zap.Int("passes", result.Passes), zap.Int("claims", result.Claims), zap.Int("deleted", result.Deleted),
						zap.Int64("due_claims", backlog.Due), zap.Duration("oldest_due_age", backlog.OldestDueAge))
				} else if result.Deleted > 0 {
					logger.Info("deleted orphan image objects",
						zap.Int("passes", result.Passes), zap.Int("claims", result.Claims), zap.Int("deleted", result.Deleted),
						zap.Int64("due_claims", backlog.Due))
				}
				if backlog.OldestDueAge > imageReconcileBacklogAlertAge && time.Since(lastBacklogAlert) >= imageReconcileBacklogAlertEvery {
					lastBacklogAlert = time.Now()
					logger.Error("image object cleanup is falling behind; check object-store deletes and the reconciler",
						zap.Int64("due_claims", backlog.Due), zap.Duration("oldest_due_age", backlog.OldestDueAge),
						zap.Int("batch", s.settings.Images.OrphanSweepBatch), zap.Duration("interval", s.settings.Images.OrphanSweepInterval))
				}
			}
		}
	}()
}
