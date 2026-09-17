package files

// ErrCodePreconditionRequired rejects client mutations that omit concurrency intent.
const ErrCodePreconditionRequired ErrorCode = "PRECONDITION_REQUIRED"

// RequireClientFilePreconditions enforces the public MCP/HTTP mutation contract.
// Initial reads are unconditional so a client can obtain a token. Internal
// transactional storage operations are not an external client compatibility path.
func RequireClientFilePreconditions(operation FileOperation, p FilePreconditions) error {
	switch operation {
	case FileOperationRead:
		return nil
	case FileOperationWrite, FileOperationRestore:
		if p.ExpectedVersion == "" && !p.CreateOnly {
			return NewError(ErrCodePreconditionRequired, "provide expected_version from file_read, or create_only=true for a new file", false)
		}
	case FileOperationDelete, FileOperationRename:
		if p.ExpectedVersion == "" {
			return NewError(ErrCodePreconditionRequired, "expected_version from file_read or file_stat is required for this file mutation", false)
		}
	default:
		return NewError(ErrCodeInvalidArgument, "unsupported file operation", false)
	}
	return nil
}
