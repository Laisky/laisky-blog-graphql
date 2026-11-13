package userrequests

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"path"
	"sort"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/Laisky/zap"
	"github.com/google/uuid"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
)

// Image object claims are rows of mcp_user_image_claims. Every object-store
// write or delete is announced by a claim committed under the image scope
// lock before the store is touched, and no transaction is held open across
// object-store I/O:
//
//   - claimUpload reserves quota for an upload in progress and protects its
//     key from deletion until the request finalizes (or the claim expires);
//   - claimDelete is a lease held while a delete of the key is in flight; new
//     uploads of the key are refused until it ends or expires, so a delayed
//     delete can never remove an object a later request uploaded;
//   - claimOrphan marks a key whose ref the GC removed; the reconciler deletes
//     the object unless something references or claims it again.
//
// Expired upload and delete claims (a crashed request, a failed delete) are
// picked up by the reconciler like orphan claims.
const (
	claimUpload = "upload"
	claimDelete = "delete"
	claimOrphan = "orphan"
)

const (
	// imageUploadClaimTTL bounds how long an upload reservation lives; it is
	// far longer than a create request may run (30 s).
	imageUploadClaimTTL = 10 * time.Minute
	// imageDeleteLeaseTTL bounds how long a delete lease lives. It must exceed
	// the time a delete request can stay in flight after the client gave up;
	// the per-delete timeout is imageObjectDeleteTimeout.
	imageDeleteLeaseTTL = 5 * time.Minute
	// imageObjectPutTimeout bounds one object upload.
	imageObjectPutTimeout = 20 * time.Second
	// imageObjectDeleteTimeout bounds one object delete.
	imageObjectDeleteTimeout = 10 * time.Second
	// imageCompensationTimeout bounds the detached cleanup of a failed create.
	// It must not depend on the request context, which is often the reason
	// the request failed.
	imageCompensationTimeout = 30 * time.Second
	// imageScopeLockNamespace salts the advisory-lock keys so they cannot
	// collide with advisory locks other components of this database take.
	imageScopeLockNamespace = "laisky-blog-graphql/mcp_user_request_images:"
)

// ImageObjectWriter uploads and removes the objects behind image refs.
// Service.CreateRequestWithImages calls PutImage only after the quota admitted
// every image, and DeleteImage only for objects that nothing references or
// claims. ImageManager is the production implementation.
type ImageObjectWriter interface {
	// PutImage stores image.PNG under image.StorageKey.
	PutImage(ctx context.Context, auth *askuser.AuthorizationContext, image UploadedImage) error
	// DeleteImage removes the object at key; deleting a missing key succeeds.
	DeleteImage(ctx context.Context, key string) error
}

// imageClaimsSchema creates the claims table. It is additive: existing image
// rows are untouched and older binaries ignore it.
var imageClaimsSchema = []string{
	`CREATE TABLE IF NOT EXISTS mcp_user_image_claims (
		id UUID PRIMARY KEY,
		owner_id UUID NOT NULL,
		user_identity VARCHAR(255) NOT NULL,
		storage_key TEXT NOT NULL,
		kind VARCHAR(16) NOT NULL,
		size_bytes BIGINT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		expires_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_mcp_user_image_claims_key ON mcp_user_image_claims (storage_key)`,
	`CREATE INDEX IF NOT EXISTS idx_mcp_user_image_claims_owner ON mcp_user_image_claims (owner_id)`,
	`CREATE INDEX IF NOT EXISTS idx_mcp_user_image_claims_user ON mcp_user_image_claims (user_identity, kind, expires_at)`,
	`CREATE INDEX IF NOT EXISTS idx_mcp_user_image_claims_expires ON mcp_user_image_claims (expires_at)`,
}

// imageTxOptions pins every image transaction to READ COMMITTED. The scope
// lock is taken by the transaction's first statement; under REPEATABLE READ
// or SERIALIZABLE (a non-default database setting) the snapshot would predate
// the lock wait and miss what the previous lock holder committed. SQLite
// ignores the level (its write lock already serializes these transactions).
var imageTxOptions = &sql.TxOptions{Isolation: sql.LevelReadCommitted}

// beginImageTx starts a short image transaction.
func (s *Service) beginImageTx(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, imageTxOptions)
	if err != nil {
		return nil, errors.Wrap(err, "begin image tx")
	}
	return tx, nil
}

// imageLockScopes returns the distinct, sorted lock scopes for keys. A scope
// is the key's directory (<prefix>/<sanitized user>), the unit that both
// quota (one user) and object ownership (identities that sanitize to the same
// segment share the directory) are serialized on.
func imageLockScopes(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	scopes := make([]string, 0, 1)
	for _, key := range keys {
		scope := path.Dir(key)
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	return scopes
}

// imageScopeLockID maps a lock scope to a PostgreSQL advisory-lock key.
// Collisions only cause extra serialization, never a missed lock.
func imageScopeLockID(scope string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(imageScopeLockNamespace + scope))
	return int64(h.Sum64()) //nolint:gosec // reinterpreting the hash bits as a lock key is intended
}

// lockImageScopes takes, for the rest of tx, the locks that serialize every
// transaction which reserves, finalizes, or releases objects under the scopes
// of keys. All of them are short and never wait on the object store.
//
// PostgreSQL uses transaction-scoped advisory locks (released at commit or
// rollback), taken in sorted order so concurrent transactions cannot
// deadlock. SQLite, which is used only in tests and local development, has no
// advisory locks; a no-op write takes its database-wide write lock instead.
func (s *Service) lockImageScopes(ctx context.Context, tx *sql.Tx, keys []string) error {
	if !s.useDollar {
		if _, err := tx.ExecContext(ctx, `UPDATE mcp_user_image_refs SET id = id WHERE 1 = 0`); err != nil {
			return errors.Wrap(err, "take sqlite write lock for image scope")
		}
		return nil
	}
	for _, scope := range imageLockScopes(keys) {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, imageScopeLockID(scope)); err != nil {
			return errors.Wrap(err, "take image scope advisory lock")
		}
	}
	return nil
}

// sqlQueryer is satisfied by both *sql.DB and *sql.Tx.
type sqlQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// inPlaceholders returns "?,?,..." and the args for values.
func inPlaceholders(values []string) (string, []any) {
	placeholders := make([]string, len(values))
	args := make([]any, len(values))
	for i, v := range values {
		placeholders[i] = "?"
		args[i] = v
	}
	return strings.Join(placeholders, ","), args
}

// queryKeySet runs query (which selects one storage_key column) and returns
// the keys it yields.
func (s *Service) queryKeySet(ctx context.Context, q sqlQueryer, query string, args ...any) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, rebindQuery(query, s.useDollar), args...)
	if err != nil {
		return nil, errors.Wrap(err, "query image keys")
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]bool)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, errors.Wrap(err, "scan image key")
		}
		out[key] = true
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate image keys")
	}
	return out, nil
}

// protectedImageKeys returns which of keys must not be deleted: any image ref
// owns them (expired or not, to stay conservative) or a live upload or delete
// claim of another owner holds them.
func (s *Service) protectedImageKeys(ctx context.Context, q sqlQueryer, keys []string, exceptOwner uuid.UUID, now time.Time) (map[string]bool, error) {
	if len(keys) == 0 {
		return map[string]bool{}, nil
	}
	in, args := inPlaceholders(keys)
	referenced, err := s.queryKeySet(ctx, q, fmt.Sprintf(`SELECT DISTINCT storage_key FROM mcp_user_image_refs WHERE storage_key IN (%s)`, in), args...)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	claimArgs := append(append([]any(nil), args...), claimUpload, claimDelete, now, exceptOwner.String())
	claimed, err := s.queryKeySet(ctx, q, fmt.Sprintf(`SELECT DISTINCT storage_key FROM mcp_user_image_claims
		WHERE storage_key IN (%s) AND kind IN (?, ?) AND expires_at > ? AND owner_id <> ?`, in), claimArgs...)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	for key := range claimed {
		referenced[key] = true
	}
	return referenced, nil
}

// insertImageClaim records one claim inside tx.
func (s *Service) insertImageClaim(ctx context.Context, tx *sql.Tx, owner uuid.UUID, userIdentity, key, kind string, size int64, now, expires time.Time) error {
	if _, err := tx.ExecContext(ctx,
		rebindQuery(`INSERT INTO mcp_user_image_claims (id, owner_id, user_identity, storage_key, kind, size_bytes, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, s.useDollar),
		gutils.UUID7Bytes().String(), owner.String(), userIdentity, key, kind, size, now, expires,
	); err != nil {
		return errors.Wrap(err, "insert image claim")
	}
	return nil
}

// distinctImageKeys returns the storage keys of images in first-seen order.
func distinctImageKeys(images []UploadedImage) []string {
	seen := make(map[string]struct{}, len(images))
	keys := make([]string, 0, len(images))
	for _, img := range images {
		if _, ok := seen[img.StorageKey]; ok {
			continue
		}
		seen[img.StorageKey] = struct{}{}
		keys = append(keys, img.StorageKey)
	}
	return keys
}

// reserveImageUploads is step 1 of a create: in one short transaction under
// the scope lock it refuses keys whose delete is in flight, checks the quota
// (live refs plus live upload claims), and records one upload claim per key,
// charged unless a live ref of the same user already holds the image.
func (s *Service) reserveImageUploads(ctx context.Context, auth *askuser.AuthorizationContext, owner uuid.UUID, images []UploadedImage) error {
	tx, err := s.beginImageTx(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	defer func() { _ = tx.Rollback() }()

	keys := distinctImageKeys(images)
	if err := s.lockImageScopes(ctx, tx, keys); err != nil {
		return errors.WithStack(err)
	}
	now := s.clock()

	in, args := inPlaceholders(keys)
	deleting, err := s.queryKeySet(ctx, tx, fmt.Sprintf(`SELECT DISTINCT storage_key FROM mcp_user_image_claims
		WHERE storage_key IN (%s) AND kind = ? AND expires_at > ?`, in), append(args, claimDelete, now)...)
	if err != nil {
		return errors.WithStack(err)
	}
	if len(deleting) > 0 {
		return errors.Wrap(ErrImageBusy, "a previous copy of this image is being deleted")
	}

	shas := make([]string, 0, len(images))
	for _, img := range images {
		shas = append(shas, img.SHA256)
	}
	shaIn, shaArgs := inPlaceholders(shas)
	liveSHAs, err := s.queryKeySet(ctx, tx, fmt.Sprintf(`SELECT sha256 FROM mcp_user_image_refs
		WHERE user_identity = ? AND sha256 IN (%s) AND expires_at > ?`, shaIn), append(append([]any{auth.UserIdentity}, shaArgs...), now)...)
	if err != nil {
		return errors.WithStack(err)
	}

	charges := make(map[string]int64, len(keys))
	var total int64
	for _, img := range images {
		if _, done := charges[img.StorageKey]; done {
			continue
		}
		charge := img.SizeBytes
		if liveSHAs[img.SHA256] {
			charge = 0 // a live ref is refreshed for free (proposal §3.5)
		}
		charges[img.StorageKey] = charge
		total += charge
	}
	if err := s.checkImageQuota(ctx, tx, auth, now, total); err != nil {
		return errors.WithStack(err)
	}
	for _, key := range keys {
		if err := s.insertImageClaim(ctx, tx, owner, auth.UserIdentity, key, claimUpload, charges[key], now, now.Add(imageUploadClaimTTL)); err != nil {
			return errors.WithStack(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, "commit image reservation")
	}
	return nil
}

// consumeUploadClaims deletes owner's live upload claims inside tx (the
// finalize step) and fails when any of the expected claims is gone, which
// means the reservation expired and its objects may already be released.
func (s *Service) consumeUploadClaims(ctx context.Context, tx *sql.Tx, owner uuid.UUID, expected int, now time.Time) error {
	res, err := tx.ExecContext(ctx,
		rebindQuery(`DELETE FROM mcp_user_image_claims WHERE owner_id = ? AND kind = ? AND expires_at > ?`, s.useDollar),
		owner.String(), claimUpload, now,
	)
	if err != nil {
		return errors.Wrap(err, "consume image upload claims")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errors.Wrap(err, "read consumed image claims")
	}
	if n != int64(expected) {
		return errors.Wrapf(ErrStorageUnavailable, "image upload reservation expired (%d of %d claims left)", n, expected)
	}
	return nil
}

// leaseUnprotectedImageKeys runs in one short transaction under the scope
// locks of lockKeys: release drops the claims the caller is giving up, then a
// delete lease is recorded for every candidate key that nothing references or
// claims, and those keys are returned. The caller deletes the objects after
// the commit, outside any transaction.
func (s *Service) leaseUnprotectedImageKeys(
	ctx context.Context,
	owner uuid.UUID,
	userIdentity string,
	lockKeys, candidates []string,
	release func(ctx context.Context, tx *sql.Tx, now time.Time) error,
) ([]string, error) {
	tx, err := s.beginImageTx(ctx)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.lockImageScopes(ctx, tx, lockKeys); err != nil {
		return nil, errors.WithStack(err)
	}
	now := s.clock()
	if err := release(ctx, tx, now); err != nil {
		return nil, errors.WithStack(err)
	}
	protected, err := s.protectedImageKeys(ctx, tx, candidates, owner, now)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	leased := make([]string, 0, len(candidates))
	for _, key := range candidates {
		if protected[key] {
			continue
		}
		if err := s.insertImageClaim(ctx, tx, owner, userIdentity, key, claimDelete, 0, now, now.Add(imageDeleteLeaseTTL)); err != nil {
			return nil, errors.WithStack(err)
		}
		leased = append(leased, key)
	}
	if err := tx.Commit(); err != nil {
		return nil, errors.Wrap(err, "commit image delete leases")
	}
	return leased, nil
}

// deleteLeasedImageObjects deletes each leased key with a bounded timeout and
// drops its lease on success. A failed (possibly still in-flight) delete
// keeps its lease until it expires, so no upload of that key can race it; the
// reconciler retries it afterwards.
func (s *Service) deleteLeasedImageObjects(ctx context.Context, objects ImageObjectWriter, owner uuid.UUID, keys []string) ([]string, error) {
	var (
		deleted []string
		failed  []error
	)
	for _, key := range keys {
		deleteCtx, cancel := context.WithTimeout(ctx, imageObjectDeleteTimeout)
		err := objects.DeleteImage(deleteCtx, key)
		cancel()
		if err != nil {
			failed = append(failed, errors.Wrapf(err, "delete %q", key))
			continue
		}
		deleted = append(deleted, key)
		if _, err := s.execContext(ctx,
			`DELETE FROM mcp_user_image_claims WHERE owner_id = ? AND storage_key = ? AND kind = ?`,
			owner.String(), key, claimDelete,
		); err != nil {
			failed = append(failed, errors.Wrapf(err, "drop delete lease of %q", key))
		}
	}
	if len(failed) > 0 {
		return deleted, errors.Join(failed...)
	}
	return deleted, nil
}

// releaseImageUploads is the compensation of a failed create. It runs
// detached from ctx with a bounded timeout: it releases the request's upload
// claims and leases every attempted key that nothing else references or
// claims (one short transaction), then deletes those objects. Failures are
// logged, not returned: the caller already returns the original error, and
// leftover claims are retried by the reconciler once they expire.
func (s *Service) releaseImageUploads(ctx context.Context, objects ImageObjectWriter, auth *askuser.AuthorizationContext, owner uuid.UUID, images []UploadedImage, attempted []string) {
	logger := gmw.GetLogger(ctx).Named("user_requests_images")
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), imageCompensationTimeout) //nolint:contextcheck // cleanup must outlive the failed request
	defer cancel()

	// Lock every key the request claimed, so its claims are released and the
	// attempted keys checked atomically.
	keys := distinctImageKeys(images)
	attemptedSet := make(map[string]bool, len(attempted))
	for _, key := range attempted {
		attemptedSet[key] = true
	}
	candidates := make([]string, 0, len(attempted))
	for _, key := range keys {
		if attemptedSet[key] {
			candidates = append(candidates, key)
		}
	}
	leased, err := s.leaseUnprotectedImageKeys(cleanupCtx, owner, auth.UserIdentity, keys, candidates,
		func(ctx context.Context, tx *sql.Tx, _ time.Time) error {
			if _, err := tx.ExecContext(ctx,
				rebindQuery(`DELETE FROM mcp_user_image_claims WHERE owner_id = ? AND kind = ?`, s.useDollar),
				owner.String(), claimUpload,
			); err != nil {
				return errors.Wrap(err, "release image upload claims")
			}
			return nil
		})
	if err != nil {
		logger.Error("release image reservation of failed request; the reconciler will retry after it expires",
			zap.Int("attempted", len(attempted)), zap.Error(err))
		return
	}
	deleted, err := s.deleteLeasedImageObjects(cleanupCtx, objects, owner, leased)
	if err != nil {
		logger.Error("delete objects of failed image request; the reconciler will retry after the lease expires",
			zap.Int("attempted", len(attempted)), zap.Int("deleted", len(deleted)), zap.Error(err))
		return
	}
	logger.Debug("released objects of failed image request",
		zap.Int("attempted", len(attempted)), zap.Int("deleted", len(deleted)))
}
