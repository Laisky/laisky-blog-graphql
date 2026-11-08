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
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
)

// imageCompensationTimeout bounds the detached cleanup that deletes the
// objects a failed create request wrote. It must not depend on the request
// context, which is often the reason the request failed.
const imageCompensationTimeout = 30 * time.Second

// imageScopeLockNamespace salts the advisory-lock keys so they cannot collide
// with advisory locks other components of this database take.
const imageScopeLockNamespace = "laisky-blog-graphql/mcp_user_request_images:"

// ImageObjectWriter uploads and removes the objects behind image refs.
// Service.CreateRequestWithImages calls PutImage only after the quota admitted
// every image, and DeleteImage only for objects that no committed ref owns.
// ImageManager is the production implementation.
type ImageObjectWriter interface {
	// PutImage stores image.PNG under image.StorageKey.
	PutImage(ctx context.Context, auth *askuser.AuthorizationContext, image UploadedImage) error
	// DeleteImage removes the object at key; deleting a missing key succeeds.
	DeleteImage(ctx context.Context, key string) error
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
// transaction which reserves, uploads, or releases objects under the scopes
// of keys. Writers hold them from quota reservation through object upload to
// commit, and cleanup holds them while it checks and deletes, so cleanup can
// never delete an object another request committed or is about to commit.
//
// PostgreSQL uses transaction-scoped advisory locks (released at commit or
// rollback), taken in sorted order so concurrent writers cannot deadlock.
// SQLite, which is used only in tests and local development, has no advisory
// locks; a no-op write takes its database-wide write lock instead.
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

// referencedImageKeys returns which of keys at least one image ref row owns,
// as seen by q. Any row counts, expired or not, so cleanup stays conservative.
func (s *Service) referencedImageKeys(ctx context.Context, q sqlQueryer, keys []string) (map[string]bool, error) {
	referenced := make(map[string]bool, len(keys))
	if len(keys) == 0 {
		return referenced, nil
	}
	placeholders := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, key := range keys {
		placeholders[i] = "?"
		args[i] = key
	}
	query := fmt.Sprintf(`SELECT DISTINCT storage_key FROM mcp_user_image_refs WHERE storage_key IN (%s)`, strings.Join(placeholders, ","))
	rows, err := q.QueryContext(ctx, rebindQuery(query, s.useDollar), args...)
	if err != nil {
		return nil, errors.Wrap(err, "query referenced image keys")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, errors.Wrap(err, "scan referenced image key")
		}
		referenced[key] = true
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate referenced image keys")
	}
	return referenced, nil
}

// deleteUnreferencedImageObjects deletes, under the scope locks, every key
// that no image ref owns and returns the deleted keys. Keys that could not
// be deleted are reported in the returned error; the rest are still deleted.
func (s *Service) deleteUnreferencedImageObjects(ctx context.Context, writer ImageObjectWriter, keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, errors.Wrap(err, "begin image cleanup tx")
	}
	// The transaction only reads; ending it releases the scope locks.
	defer func() { _ = tx.Rollback() }()

	if err := s.lockImageScopes(ctx, tx, keys); err != nil {
		return nil, errors.WithStack(err)
	}
	referenced, err := s.referencedImageKeys(ctx, tx, keys)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	var (
		deleted []string
		failed  []error
	)
	for _, key := range keys {
		if referenced[key] {
			continue
		}
		if err := writer.DeleteImage(ctx, key); err != nil {
			failed = append(failed, errors.Wrapf(err, "delete %q", key))
			continue
		}
		deleted = append(deleted, key)
	}
	if len(failed) > 0 {
		return deleted, errors.Join(failed...)
	}
	return deleted, nil
}

// releaseFailedImageUploads is the compensation step of
// CreateRequestWithImages: after the transaction rolled back (or its commit
// failed, an ambiguous outcome) it deletes every attempted key that no
// committed ref owns. It runs detached from ctx with a bounded timeout, and
// only logs failures: the caller already returns the original error, and the
// orphan reconciler and the bucket lifecycle rule remove anything left.
func (s *Service) releaseFailedImageUploads(ctx context.Context, writer ImageObjectWriter, attempted []string) {
	logger := gmw.GetLogger(ctx).Named("user_requests_images")
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), imageCompensationTimeout) //nolint:contextcheck // cleanup must outlive the failed request
	defer cancel()

	deleted, err := s.deleteUnreferencedImageObjects(cleanupCtx, writer, attempted)
	if err != nil {
		logger.Error("release objects of failed image request; the orphan reconciler will retry",
			zap.Int("attempted", len(attempted)),
			zap.Int("deleted", len(deleted)),
			zap.Error(err),
		)
		return
	}
	logger.Debug("released objects of failed image request",
		zap.Int("attempted", len(attempted)),
		zap.Int("deleted", len(deleted)),
	)
}
