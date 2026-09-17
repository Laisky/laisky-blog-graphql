package files

import (
	"context"
	"database/sql"
	"encoding/hex"
	"strconv"
	"strings"

	errors "github.com/Laisky/errors/v2"
)

const (
	// ErrCodeVersionConflict rejects an obsolete read/edit base without side effects.
	ErrCodeVersionConflict ErrorCode = "VERSION_CONFLICT"
	// ErrCodeInvalidContent rejects bytes that cannot be represented losslessly as UTF-8.
	ErrCodeInvalidContent ErrorCode = "INVALID_CONTENT"
	// ErrCodeRevisionExhausted rejects a mutation rather than wrapping its revision.
	ErrCodeRevisionExhausted ErrorCode = "REVISION_EXHAUSTED"
)

// FileOperation identifies the single operation protected by FilePreconditions.
type FileOperation string

const (
	// FileOperationRead protects one range read.
	FileOperationRead FileOperation = "read"
	// FileOperationWrite protects one content mutation.
	FileOperationWrite FileOperation = "write"
	// FileOperationDelete protects deletion of one file, not a directory read set.
	FileOperationDelete FileOperation = "delete"
	// FileOperationRename protects a file source and its destination.
	FileOperationRename FileOperation = "rename"
	// FileOperationRestore protects the current file when restoring historical bytes.
	FileOperationRestore FileOperation = "restore"
)

// FilePreconditions carries optimistic concurrency conditions, not authorization.
// Public MCP/HTTP mutation adapters require conditions. Empty conditions are
// limited to initial reads and internal imperative storage calls. A file token
// never represents an entire directory; retry deduplication is separate.
type FilePreconditions struct {
	ExpectedVersion            string
	CreateOnly                 bool
	DestinationPath            string
	ExpectedDestinationVersion string
	DestinationMustNotExist    bool
}

// Empty reports whether the request has no concurrency conditions.
func (p FilePreconditions) Empty() bool {
	return p.ExpectedVersion == "" && !p.CreateOnly && p.ExpectedDestinationVersion == "" && !p.DestinationMustNotExist
}

type filePreconditionKey struct{}

type scopedFilePreconditions struct {
	operation                       FileOperation
	apiKeyHash, project, path, owner string
	conditions                      FilePreconditions
}

// ValidateFileVersion validates the opaque incarnation:revision token without
// ever passing a revision through a floating-point JSON number.
func ValidateFileVersion(version string) error {
	identity, revision, ok := strings.Cut(version, ":")
	if !ok || len(identity) != 32 || len(revision) == 0 || len(revision) > 19 {
		return NewError(ErrCodeInvalidArgument, "expected_version must be an opaque file version returned by read or stat", false)
	}
	if _, err := hex.DecodeString(identity); err != nil {
		return NewError(ErrCodeInvalidArgument, "invalid file incarnation", false)
	}
	n, err := strconv.ParseInt(revision, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != revision {
		return NewError(ErrCodeInvalidArgument, "invalid file revision", false)
	}
	return nil
}

// WithFilePreconditions binds conditions to one operation and authenticated file
// scope. Plugin adapters can forward the context without changing the legacy
// Plugin interface; internal index/state operations cannot inherit user-file CAS.
// Callers must check the error, and use canonical service paths (leading slash).
func WithFilePreconditions(ctx context.Context, auth AuthContext, project, path string, operation FileOperation, p FilePreconditions) (context.Context, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	switch operation {
	case FileOperationRead, FileOperationWrite, FileOperationDelete, FileOperationRename, FileOperationRestore:
	default:
		return nil, errors.WithStack(NewError(ErrCodeInvalidArgument, "invalid conditional file operation", false))
	}
	if p.ExpectedVersion != "" {
		if err := ValidateFileVersion(p.ExpectedVersion); err != nil {
			return nil, errors.WithStack(err)
		}
	}
	if p.CreateOnly && p.ExpectedVersion != "" {
		return nil, errors.WithStack(NewError(ErrCodeInvalidArgument, "expected_version and create_only are mutually exclusive", false))
	}
	if p.CreateOnly && operation != FileOperationWrite && operation != FileOperationRestore {
		return nil, errors.WithStack(NewError(ErrCodeInvalidArgument, "create_only is only valid for write or restore", false))
	}
	if p.ExpectedDestinationVersion != "" {
		if err := ValidateFileVersion(p.ExpectedDestinationVersion); err != nil {
			return nil, errors.WithStack(err)
		}
	}
	if p.ExpectedDestinationVersion != "" && p.DestinationMustNotExist {
		return nil, errors.WithStack(NewError(ErrCodeInvalidArgument, "destination conditions are mutually exclusive", false))
	}
	if p.ExpectedDestinationVersion != "" || p.DestinationMustNotExist {
		if operation != FileOperationRename || p.ExpectedVersion == "" || p.DestinationPath == "" {
			return nil, errors.WithStack(NewError(ErrCodeInvalidArgument, "destination conditions require a conditional file rename", false))
		}
	}
	return context.WithValue(ctx, filePreconditionKey{}, scopedFilePreconditions{
		operation: operation, apiKeyHash: auth.APIKeyHash, project: project,
		path: path, owner: systemOwnerFromContext(ctx), conditions: p,
	}), nil
}

func filePreconditionsFromContext(ctx context.Context, auth AuthContext, project, path string, operation FileOperation) FilePreconditions {
	p, ok := ctx.Value(filePreconditionKey{}).(scopedFilePreconditions)
	if !ok || p.operation != operation || p.apiKeyHash != auth.APIKeyHash || p.project != project || p.path != path || p.owner != systemOwnerFromContext(ctx) {
		return FilePreconditions{}
	}
	return p.conditions
}

func checkFileVersion(actual, expected string, createOnly bool) error {
	if (expected != "" && actual != expected) || (createOnly && actual != "") {
		return NewError(ErrCodeVersionConflict, "file version changed; re-read the file and recompute the edit before retrying", false)
	}
	return nil
}

func (s *Service) fileVersionTx(ctx context.Context, tx *sql.Tx, auth AuthContext, project, path string) (string, error) {
	var version string
	err := tx.QueryRowContext(ctx, rebindSQL(`SELECT incarnation_id || ':' || CAST(revision AS TEXT)
		FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE AND system_owner = ?`, s.isPostgres),
		auth.APIKeyHash, project, path, systemOwnerFromContext(ctx)).Scan(&version)
	if err != nil {
		return "", errors.Wrap(err, "read live file version")
	}
	return version, nil
}

func (s *Service) checkPathVersionTx(ctx context.Context, tx *sql.Tx, auth AuthContext, project, path, expected string, createOnly bool) error {
	if expected == "" && !createOnly {
		return nil
	}
	actual, err := s.fileVersionTx(ctx, tx, auth, project, path)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return checkFileVersion(actual, expected, createOnly)
}

func normalizeRevisionError(err error) error {
	if err != nil && strings.Contains(err.Error(), "FILEIO_REVISION_EXHAUSTED") {
		return NewError(ErrCodeRevisionExhausted, "file revision exhausted; mutation was not committed", false)
	}
	return err
}
