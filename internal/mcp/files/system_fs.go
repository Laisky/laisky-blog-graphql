// SystemFS is the proposal §2.8 internal-only handle that lets a system plugin
// such as pageindex_plugin own a private namespace of file rows without going
// through the plugin manager. The handle is bound to one non-empty system_owner
// for its lifetime; SQL scoping inside *Service uses that owner instead of "".
package files

import (
	"context"
	"strings"

	errors "github.com/Laisky/errors/v2"
)

// SystemFS is a restricted FS handle bound to a single non-empty system_owner.
// It is constructed via Service.SystemNamespace(owner). It is never registered
// in the plugin manager and never reachable from MCP tools.
type SystemFS interface {
	Read(ctx context.Context, project, path string) ([]byte, error)
	Write(ctx context.Context, project, path string, content []byte) error
	Delete(ctx context.Context, project, path string) error
	List(ctx context.Context, project, prefix string) ([]string, error)
}

// systemFS is the concrete implementation. The owner string is captured at
// construction so callers cannot widen the handle back into the user namespace.
type systemFS struct {
	svc   *Service
	owner string
}

// SystemNamespace returns a SystemFS handle bound to the supplied owner string.
// Owner must be a non-empty stable identifier such as "pageindex".
func (s *Service) SystemNamespace(owner string) (SystemFS, error) {
	if s == nil {
		return nil, errors.New("service is nil")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, errors.New("system_owner must be non-empty")
	}
	return &systemFS{svc: s, owner: owner}, nil
}

// systemAuth returns a synthetic AuthContext keyed by the system owner. The
// "system:<owner>" prefix means SQL bucketing is deterministic per owner and
// never mingles with any tenant's apikey_hash.
func (s *systemFS) systemAuth() AuthContext {
	return AuthContext{APIKeyHash: "system:" + s.owner}
}

// Read returns file content from the system namespace.
func (s *systemFS) Read(ctx context.Context, project, path string) ([]byte, error) {
	ctx = contextWithSystemOwner(ctx, s.owner)
	res, err := s.svc.Read(ctx, s.systemAuth(), project, path, 0, -1)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return []byte(res.Content), nil
}

// Write replaces the content at path within the system namespace. Mode is
// always TRUNCATE — system data is JSON keyed lookups, not partial offsets.
func (s *systemFS) Write(ctx context.Context, project, path string, content []byte) error {
	ctx = contextWithSystemOwner(ctx, s.owner)
	if _, err := s.svc.WriteWith(ctx, s.systemAuth(), project, path, string(content), "utf8", 0, WriteModeTruncate, WriteOpts{SystemOwner: s.owner}); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

// Delete removes a system-namespace file. Recursive deletion is intentionally
// disallowed; system callers either Write a new key or Delete one path at a time.
func (s *systemFS) Delete(ctx context.Context, project, path string) error {
	ctx = contextWithSystemOwner(ctx, s.owner)
	if _, err := s.svc.Delete(ctx, s.systemAuth(), project, path, false); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

// List returns paths beneath prefix in the system namespace. Only paths are
// returned: system data is keyed lookups, callers do not need entry metadata.
func (s *systemFS) List(ctx context.Context, project, prefix string) ([]string, error) {
	ctx = contextWithSystemOwner(ctx, s.owner)
	res, err := s.svc.List(ctx, s.systemAuth(), project, prefix, 8, 1000)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	paths := make([]string, 0, len(res.Entries))
	for _, e := range res.Entries {
		if e.Type == FileTypeFile {
			paths = append(paths, e.Path)
		}
	}
	return paths, nil
}
