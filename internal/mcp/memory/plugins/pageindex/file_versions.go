package pageindex

import "github.com/Laisky/laisky-blog-graphql/internal/mcp/files"

// SupportsFileVersionPreconditions requires the transactional system handle so
// rejected lifecycle operations cannot first mutate a non-transactional catalog.
func (p *Plugin) SupportsFileVersionPreconditions() bool {
	_, ok := p.sysFS.(files.AtomicSystemFS)
	return ok
}
