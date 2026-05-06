package plugin

import (
	"context"
	"sort"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// Manager resolves per-call plugin selection and forwards file operations.
type Manager struct {
	plugins       map[string]Plugin
	defaultPlugin string
}

// NewManager constructs a plugin manager with a validated default plugin.
func NewManager(defaultPlugin string, plugins ...Plugin) (*Manager, error) {
	if len(plugins) == 0 {
		return nil, errors.New("at least one plugin is required")
	}

	plugs := make(map[string]Plugin, len(plugins))
	for _, item := range plugins {
		if item == nil {
			return nil, errors.New("plugin is nil")
		}

		name := NormalizeName(item.Name())
		if name == "" {
			return nil, errors.New("plugin name is empty")
		}
		if _, exists := plugs[name]; exists {
			return nil, errors.Errorf("duplicate plugin %q", name)
		}

		plugs[name] = item
	}

	resolvedDefault := NormalizeName(defaultPlugin)
	if resolvedDefault == "" || resolvedDefault == DefaultPluginAuto {
		resolvedDefault = DefaultPluginRAG
	}

	if _, exists := plugs[resolvedDefault]; !exists {
		return nil, errors.Errorf("default plugin %q is not registered", resolvedDefault)
	}

	return &Manager{
		plugins:       plugs,
		defaultPlugin: resolvedDefault,
	}, nil
}

// DefaultPlugin returns the configured default plugin name.
func (m *Manager) DefaultPlugin() string {
	if m == nil {
		return ""
	}

	return m.defaultPlugin
}

// Name reports the manager's identity for plugin.Plugin compatibility.
func (m *Manager) Name() string {
	return m.DefaultPlugin()
}

// Capabilities returns the default plugin's capabilities; per-call routing surfaces real ones.
func (m *Manager) Capabilities() Capabilities {
	if m == nil {
		return Capabilities{}
	}
	if item, exists := m.plugins[m.defaultPlugin]; exists {
		return item.Capabilities()
	}
	return Capabilities{}
}

// Start delegates to StartAll for plugin.Plugin compatibility.
func (m *Manager) Start(ctx context.Context) error {
	return m.StartAll(ctx)
}

// Stop delegates to StopAll for plugin.Plugin compatibility.
func (m *Manager) Stop(ctx context.Context) error {
	return m.StopAll(ctx)
}

// AvailablePlugins returns the sorted set of registered plugin names.
func (m *Manager) AvailablePlugins() []string {
	if m == nil || len(m.plugins) == 0 {
		return nil
	}

	names := make([]string, 0, len(m.plugins))
	for name := range m.plugins {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Resolve selects a plugin using the per-call override or the configured default.
func (m *Manager) Resolve(ctx context.Context, _ files.AuthContext, _ string, override string) (Plugin, error) {
	if m == nil {
		return nil, errors.New("plugin manager is nil")
	}

	requested := NormalizeName(override)
	if requested == "" {
		requested = OverrideFromContext(ctx)
	}
	if requested == "" || requested == DefaultPluginAuto {
		requested = m.defaultPlugin
	}

	item, exists := m.plugins[requested]
	if !exists {
		return nil, &ResolveError{Requested: requested, Available: m.AvailablePlugins()}
	}

	return item, nil
}

// ForName returns a plugin by name for tests and admin callers.
func (m *Manager) ForName(name string) (Plugin, error) {
	if m == nil {
		return nil, errors.New("plugin manager is nil")
	}

	item, exists := m.plugins[NormalizeName(name)]
	if !exists {
		return nil, &ResolveError{Requested: NormalizeName(name), Available: m.AvailablePlugins()}
	}

	return item, nil
}

// StartAll starts every registered plugin in a deterministic order.
func (m *Manager) StartAll(ctx context.Context) error {
	for _, name := range m.AvailablePlugins() {
		item := m.plugins[name]
		if err := item.Start(ctx); err != nil {
			return errors.Wrapf(err, "start plugin %s", name)
		}
	}

	return nil
}

// StopAll stops every registered plugin in a deterministic order.
func (m *Manager) StopAll(ctx context.Context) error {
	for _, name := range m.AvailablePlugins() {
		item := m.plugins[name]
		if err := item.Stop(ctx); err != nil {
			return errors.Wrapf(err, "stop plugin %s", name)
		}
	}

	return nil
}

// Stat routes file_stat to the selected plugin.
func (m *Manager) Stat(ctx context.Context, auth files.AuthContext, project, path string) (files.StatResult, error) {
	item, err := m.Resolve(ctx, auth, project, "")
	if err != nil {
		return files.StatResult{}, err
	}

	return item.Stat(ctx, auth, project, path)
}

// Read routes file_read to the selected plugin.
func (m *Manager) Read(ctx context.Context, auth files.AuthContext, project, path string, offset, length int64) (files.ReadResult, error) {
	item, err := m.Resolve(ctx, auth, project, "")
	if err != nil {
		return files.ReadResult{}, err
	}

	return item.Read(ctx, auth, project, path, offset, length)
}

// Write routes file_write to the selected plugin.
func (m *Manager) Write(ctx context.Context, auth files.AuthContext, project, path, content, contentEncoding string, offset int64, mode files.WriteMode) (files.WriteResult, error) {
	item, err := m.Resolve(ctx, auth, project, "")
	if err != nil {
		return files.WriteResult{}, err
	}

	return item.Write(ctx, auth, project, path, content, contentEncoding, offset, mode)
}

// Delete routes file_delete to the selected plugin.
func (m *Manager) Delete(ctx context.Context, auth files.AuthContext, project, path string, recursive bool) (files.DeleteResult, error) {
	item, err := m.Resolve(ctx, auth, project, "")
	if err != nil {
		return files.DeleteResult{}, err
	}

	return item.Delete(ctx, auth, project, path, recursive)
}

// Rename routes file_rename to the selected plugin.
func (m *Manager) Rename(ctx context.Context, auth files.AuthContext, project, fromPath, toPath string, overwrite bool) (files.RenameResult, error) {
	item, err := m.Resolve(ctx, auth, project, "")
	if err != nil {
		return files.RenameResult{}, err
	}

	return item.Rename(ctx, auth, project, fromPath, toPath, overwrite)
}

// List routes file_list to the selected plugin.
func (m *Manager) List(ctx context.Context, auth files.AuthContext, project, path string, depth, limit int) (files.ListResult, error) {
	item, err := m.Resolve(ctx, auth, project, "")
	if err != nil {
		return files.ListResult{}, err
	}

	return item.List(ctx, auth, project, path, depth, limit)
}

// Search routes file_search to the selected plugin.
func (m *Manager) Search(ctx context.Context, auth files.AuthContext, project, query, pathPrefix string, limit int) (files.SearchResult, error) {
	item, err := m.Resolve(ctx, auth, project, "")
	if err != nil {
		return files.SearchResult{}, err
	}

	return item.Search(ctx, auth, project, query, pathPrefix, limit)
}
