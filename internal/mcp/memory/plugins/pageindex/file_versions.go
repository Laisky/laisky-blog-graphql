package pageindex

// SupportsFileVersionPreconditions advertises scoped conditions enforced by the
// shared FileIO service before PageIndex indexing or system-state publication.
func (*Plugin) SupportsFileVersionPreconditions() bool { return true }
