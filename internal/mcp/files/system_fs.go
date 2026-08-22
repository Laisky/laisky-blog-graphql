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

// AtomicSystemFS extends SystemFS with transaction-scoped mutations that can
// commit a user file operation and the owner's system catalog together.
type AtomicSystemFS interface {
	SystemFS
	PublishPluginSummaryAndState(ctx context.Context, auth AuthContext, project, systemProject, path string, in PluginSummaryInput, mutate SystemStateMutator) (bool, error)
	DeleteWithState(ctx context.Context, auth AuthContext, project, systemProject, path string, recursive bool, mutate SystemStateMutator) (DeleteResult, error)
	RenameWithState(ctx context.Context, auth AuthContext, project, systemProject, fromPath, toPath string, overwrite bool, mutate SystemStateMutator) (RenameResult, error)
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
	if _, err := s.svc.WriteWith(ctx, s.systemAuth(), project, path, string(content), "utf-8", 0, WriteModeTruncate, WriteOpts{SystemOwner: s.owner}); err != nil {
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

// PublishPluginSummaryAndState atomically publishes user summary metadata and
// system-owned plugin state under the authenticated project lock.
func (s *systemFS) PublishPluginSummaryAndState(ctx context.Context, auth AuthContext, project, systemProject, path string, in PluginSummaryInput, mutate SystemStateMutator) (bool, error) {
	ctx = contextWithSystemOwner(ctx, s.owner)
	published, err := s.svc.PublishPluginSummaryAndState(ctx, auth, project, systemProject, path, in, mutate)
	if err != nil {
		return false, errors.WithStack(err)
	}
	return published, nil
}

// DeleteWithState atomically deletes user files and system-owned plugin state.
func (s *systemFS) DeleteWithState(ctx context.Context, auth AuthContext, project, systemProject, path string, recursive bool, mutate SystemStateMutator) (DeleteResult, error) {
	ctx = contextWithSystemOwner(ctx, s.owner)
	result, err := s.svc.DeleteWithSystemState(ctx, auth, project, systemProject, path, recursive, mutate)
	if err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	return result, nil
}

// RenameWithState atomically renames user files and system-owned plugin state.
func (s *systemFS) RenameWithState(ctx context.Context, auth AuthContext, project, systemProject, fromPath, toPath string, overwrite bool, mutate SystemStateMutator) (RenameResult, error) {
	ctx = contextWithSystemOwner(ctx, s.owner)
	result, err := s.svc.RenameWithSystemState(ctx, auth, project, systemProject, fromPath, toPath, overwrite, mutate)
	if err != nil {
		return RenameResult{}, errors.WithStack(err)
	}
	return result, nil
}
