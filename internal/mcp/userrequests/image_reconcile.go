package userrequests

import (
	"context"
	"path"
	"sort"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/storage"
)

// imageReconcileTimeout bounds one reconciler pass.
const imageReconcileTimeout = 5 * time.Minute

// minImageOrphanGracePeriod is the smallest accepted grace period. It must
// comfortably exceed the create request deadline so an upload whose
// transaction is still open is never mistaken for an orphan by age alone.
const minImageOrphanGracePeriod = time.Minute

// ImageReconcileResult summarizes one ReconcileImageObjects pass.
type ImageReconcileResult struct {
	// Scanned is how many objects the pass listed.
	Scanned int
	// Candidates is how many listed objects were managed image keys older
	// than the grace period.
	Candidates int
	// Deleted is how many candidates no image ref owned and were deleted.
	Deleted int
	// NextCursor is the key the next pass resumes after; empty once the
	// listing is exhausted, so the next pass starts over.
	NextCursor string
}

// listImageObjects lists objects under the image prefix after startAfter.
func (m *ImageManager) listImageObjects(ctx context.Context, startAfter string, limit int) ([]storage.ObjectInfo, error) {
	if m == nil || m.store == nil {
		return nil, errors.Wrap(ErrStorageUnavailable, "image manager unconfigured")
	}
	objects, err := m.store.List(ctx, m.settings.Prefix+"/", startAfter, limit)
	if err != nil {
		return nil, errors.Wrap(err, "list image objects")
	}
	return objects, nil
}

// isManagedImageKey reports whether key has exactly the shape buildKey
// produces (<prefix>/<segment>/<64 hex>.png). The reconciler never touches
// anything else, even under the image prefix.
func (m *ImageManager) isManagedImageKey(key string) bool {
	rest, ok := strings.CutPrefix(key, m.settings.Prefix+"/")
	if !ok {
		return false
	}
	segment, file, ok := strings.Cut(rest, "/")
	if !ok || segment == "" || strings.Contains(file, "/") || sanitizeKeySegment(segment) != segment {
		return false
	}
	sha, ok := strings.CutSuffix(file, ".png")
	if !ok || len(sha) != 64 {
		return false
	}
	for i := 0; i < len(sha); i++ {
		c := sha[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ReconcileImageObjects is one bounded pass of the orphan-object reconciler.
// It lists up to OrphanSweepBatch objects after cursor and deletes each
// managed image object that is older than OrphanGracePeriod and that no image
// ref owns. Ownership is re-checked under the image scope lock right before
// the delete, so an object a request is reserving or committing is never
// removed. It backstops compensation for objects left by crashes or failed
// compensating deletes, and objects of refs the GC expired, even when the
// bucket lifecycle rule could not be installed.
func (s *Service) ReconcileImageObjects(ctx context.Context, manager *ImageManager, cursor string) (ImageReconcileResult, error) {
	var result ImageReconcileResult
	if manager == nil {
		return result, errors.Wrap(ErrStorageUnavailable, "image manager unconfigured")
	}
	batch := s.settings.Images.OrphanSweepBatch
	if batch <= 0 {
		batch = DefaultImageOrphanSweepBatch
	}
	grace := s.settings.Images.OrphanGracePeriod
	if grace < minImageOrphanGracePeriod {
		grace = DefaultImageOrphanGracePeriod
	}

	objects, err := manager.listImageObjects(ctx, cursor, batch)
	if err != nil {
		return result, errors.WithStack(err)
	}
	result.Scanned = len(objects)
	if len(objects) == batch {
		result.NextCursor = objects[len(objects)-1].Key
	}

	cutoff := s.clock().Add(-grace)
	candidates := make([]string, 0)
	for _, obj := range objects {
		if !manager.isManagedImageKey(obj.Key) || obj.LastModified.IsZero() || obj.LastModified.After(cutoff) {
			continue
		}
		candidates = append(candidates, obj.Key)
	}
	result.Candidates = len(candidates)
	if len(candidates) == 0 {
		return result, nil
	}

	// Cheap unlocked pre-filter: most objects are referenced. Unreferenced
	// ones are re-checked under the scope lock before anything is deleted.
	referenced, err := s.referencedImageKeys(ctx, s.db, candidates)
	if err != nil {
		return result, errors.WithStack(err)
	}
	byScope := make(map[string][]string)
	for _, key := range candidates {
		if !referenced[key] {
			byScope[path.Dir(key)] = append(byScope[path.Dir(key)], key)
		}
	}
	scopes := make([]string, 0, len(byScope))
	for scope := range byScope {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)

	var failed []error
	for _, scope := range scopes {
		deleted, deleteErr := s.deleteUnreferencedImageObjects(ctx, manager, byScope[scope])
		result.Deleted += len(deleted)
		if deleteErr != nil {
			failed = append(failed, errors.Wrapf(deleteErr, "reconcile scope %q", scope))
		}
	}
	if len(failed) > 0 {
		return result, errors.Join(failed...)
	}
	return result, nil
}

// StartImageReconcileWorker runs ReconcileImageObjects every
// OrphanSweepInterval until ctx is canceled. Each pass resumes after the last
// key of the previous one, so a large bucket is covered over several passes.
func (s *Service) StartImageReconcileWorker(ctx context.Context, manager *ImageManager) {
	if s == nil || manager == nil || s.settings.Images.OrphanSweepInterval <= 0 {
		return
	}
	logger := s.log().Named("image_reconciler")
	go func() {
		ticker := time.NewTicker(s.settings.Images.OrphanSweepInterval)
		defer ticker.Stop()
		cursor := ""
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				passCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), imageReconcileTimeout) //nolint:contextcheck // detached context for background sweep
				result, err := s.ReconcileImageObjects(passCtx, manager, cursor)
				cancel()
				cursor = result.NextCursor
				if err != nil {
					logger.Error("reconcile orphan image objects", zap.Error(err),
						zap.Int("scanned", result.Scanned), zap.Int("deleted", result.Deleted))
					continue
				}
				if result.Deleted > 0 {
					logger.Info("deleted orphan image objects",
						zap.Int("scanned", result.Scanned), zap.Int("deleted", result.Deleted))
				}
			}
		}
	}()
}
