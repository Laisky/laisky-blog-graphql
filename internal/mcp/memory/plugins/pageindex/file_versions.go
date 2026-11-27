package pageindex

// SupportsFileVersionPreconditions reports false: the dormant plugin updates
// its catalog outside the FileIO transaction, so a rejected lifecycle
// operation could not be rolled back together with it.
func (p *Plugin) SupportsFileVersionPreconditions() bool {
	return false
}
