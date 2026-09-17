package rag

// SupportsFileVersionPreconditions advertises scoped conditions enforced by the
// shared FileIO service. Internal indexing does not inherit user-write conditions.
func (*Plugin) SupportsFileVersionPreconditions() bool { return true }
