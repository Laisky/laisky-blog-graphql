// Strategy A (proposal §4.2): FileService is a deprecation alias of mcpplugin.Plugin.
// Callers should depend on mcpplugin.Plugin or *mcpplugin.Manager directly. Kept as a
// source-compat alias for one minor version.
package tools

import mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"

// Deprecated: use mcpplugin.Plugin or *mcpplugin.Manager directly.
type FileService = mcpplugin.Plugin
