// Tests for the conformance suite's cross-plugin scenarios (C23/C24/C26/R09).
// These exercise the suite end-to-end against an in-memory fake manager that
// owns two memstore-backed plugins. The fakes intentionally implement the
// minimum surface needed by the cross-plugin suite — they are not a public
// reference implementation.
package conformance

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// memStore is a goroutine-safe (project, path) → bytes map.
type memStore struct {
	mu   sync.Mutex
	data map[string]map[string]string
	// owners records which plugin name owns each (project, path) globally
	// across the registry so cross-plugin reads can name the owner. Shared
	// by all memPlugins constructed against the same memOwners registry.
	owners *memOwners
	name   string
}

// memOwners tracks plugin ownership for cross-plugin NOT_FOUND hints.
type memOwners struct {
	mu  sync.Mutex
	own map[string]string // key = project + "\x00" + path → owner plugin name
}

func newOwners() *memOwners { return &memOwners{own: map[string]string{}} }

func (o *memOwners) set(project, path, owner string) {
	o.mu.Lock()
	o.own[project+"\x00"+path] = owner
	o.mu.Unlock()
}

func (o *memOwners) get(project, path string) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	v, ok := o.own[project+"\x00"+path]
	return v, ok
}

// memPlugin is a contract-shaped fake plugin backed by a memStore. Per-plugin
// stores preserve the cross-plugin invariant: a write under plugin=A never
// surfaces in a read under plugin=B; instead pluginB's read returns
// NOT_FOUND and (when known) names the owner.
type memPlugin struct {
	name  string
	store *memStore
}

func newMemPlugin(name string, owners *memOwners) *memPlugin {
	return &memPlugin{
		name: name,
		store: &memStore{
			data:   map[string]map[string]string{},
			owners: owners,
			name:   name,
		},
	}
}

func (p *memPlugin) Name() string { return p.name }
func (p *memPlugin) Capabilities() mcpplugin.Capabilities {
	return mcpplugin.Capabilities{}
}
func (p *memPlugin) Start(context.Context) error { return nil }
func (p *memPlugin) Stop(context.Context) error  { return nil }

func (p *memPlugin) Stat(_ context.Context, _ files.AuthContext, project, path string) (files.StatResult, error) {
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	if bucket, ok := p.store.data[project]; ok {
		if body, ok := bucket[path]; ok {
			return files.StatResult{Exists: true, Type: files.FileTypeFile, Size: int64(len(body))}, nil
		}
	}
	return files.StatResult{}, p.notFound(project, path)
}

func (p *memPlugin) Read(_ context.Context, _ files.AuthContext, project, path string, _, _ int64) (files.ReadResult, error) {
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	if bucket, ok := p.store.data[project]; ok {
		if body, ok := bucket[path]; ok {
			return files.ReadResult{Content: body, ContentEncoding: "utf-8"}, nil
		}
	}
	return files.ReadResult{}, p.notFound(project, path)
}

func (p *memPlugin) Write(_ context.Context, _ files.AuthContext, project, path, content, _ string, _ int64, _ files.WriteMode) (files.WriteResult, error) {
	p.store.mu.Lock()
	if _, ok := p.store.data[project]; !ok {
		p.store.data[project] = map[string]string{}
	}
	p.store.data[project][path] = content
	p.store.mu.Unlock()
	if p.store.owners != nil {
		p.store.owners.set(project, path, p.name)
	}
	return files.WriteResult{BytesWritten: int64(len(content))}, nil
}

func (p *memPlugin) Delete(context.Context, files.AuthContext, string, string, bool) (files.DeleteResult, error) {
	return files.DeleteResult{}, nil
}
func (p *memPlugin) Rename(context.Context, files.AuthContext, string, string, string, bool) (files.RenameResult, error) {
	return files.RenameResult{}, nil
}
func (p *memPlugin) List(context.Context, files.AuthContext, string, string, int, int) (files.ListResult, error) {
	return files.ListResult{}, nil
}
func (p *memPlugin) Search(context.Context, files.AuthContext, string, string, string, int) (files.SearchResult, error) {
	return files.SearchResult{}, nil
}

// notFound returns a typed NOT_FOUND error and, when the path is owned by a
// different plugin, embeds that plugin's name so cross-plugin scenarios can
// surface the owner hint.
func (p *memPlugin) notFound(project, path string) error {
	if p.store.owners != nil {
		if owner, ok := p.store.owners.get(project, path); ok && owner != p.name {
			return files.NewError(files.ErrCodeNotFound, fmt.Sprintf("not_found under plugin=%q; path is owned by plugin=%q", p.name, owner), false)
		}
	}
	return files.NewError(files.ErrCodeNotFound, fmt.Sprintf("not_found under plugin=%q", p.name), false)
}

// crossPluginFixture wires two memPlugins and a manager so the conformance
// suite can drive C23/C24/C26/R09 without a database.
type crossPluginFixture struct {
	manager   *mcpplugin.Manager
	primary   *memPlugin
	secondary *memPlugin
	def       string
}

func newCrossPluginFixture(t *testing.T, defaultName, primaryName, secondaryName string) *crossPluginFixture {
	t.Helper()
	owners := newOwners()
	primary := newMemPlugin(primaryName, owners)
	secondary := newMemPlugin(secondaryName, owners)
	mgr, err := mcpplugin.NewManager(defaultName, primary, secondary)
	if err != nil {
		t.Fatalf("build manager: %v", err)
	}
	return &crossPluginFixture{manager: mgr, primary: primary, secondary: secondary, def: defaultName}
}

func (f *crossPluginFixture) Plugin() mcpplugin.Plugin          { return f.primary }
func (f *crossPluginFixture) SecondaryPlugin() mcpplugin.Plugin { return f.secondary }
func (f *crossPluginFixture) Manager() *mcpplugin.Manager       { return f.manager }
func (f *crossPluginFixture) DefaultPluginName() string         { return f.def }
func (f *crossPluginFixture) HasStorage() bool                  { return true }
func (f *crossPluginFixture) Cleanup(*testing.T)                {}
func (f *crossPluginFixture) NewAuthContext(t *testing.T) mcpplugin.AuthContext {
	t.Helper()
	return mcpplugin.AuthContext{APIKey: "k", APIKeyHash: "h", UserIdentity: "u:" + t.Name()}
}
func (f *crossPluginFixture) NewProject(t *testing.T) string {
	t.Helper()
	return "proj-" + t.Name()
}

// TestCrossPluginSuite exercises C23, C24, C26, and R09 in isolation against
// the in-memory cross-plugin fixture. The single-plugin no-storage fixtures
// in the rag and pageindex packages skip these scenarios via
// Options.SkipCrossPlugin.
func TestCrossPluginSuite(t *testing.T) {
	t.Parallel()

	fx := newCrossPluginFixture(t, mcpplugin.DefaultPluginRAG, mcpplugin.DefaultPluginRAG, mcpplugin.DefaultPluginPageIndex)

	t.Run("C23", func(t *testing.T) { runC23(t, fx, Options{}) })
	t.Run("C24", func(t *testing.T) { runC24(t, fx, Options{}) })
	t.Run("C26", func(t *testing.T) { runC26(t, fx, Options{}) })
	t.Run("R09", func(t *testing.T) { runR09(t, fx, Options{}) })
}

// TestCrossPluginSkipsSinglePlugin verifies the cross-plugin scenarios skip
// cleanly against a fixture that does not implement MultiPluginFixture.
func TestCrossPluginSkipsSinglePlugin(t *testing.T) {
	t.Parallel()

	fx := &singleFixture{}
	for _, sc := range []struct {
		name string
		fn   func(*testing.T, Fixture, Options)
	}{
		{"C23", runC23},
		{"C24", runC24},
		{"C26", runC26},
		{"R09", runR09},
	} {
		t.Run(sc.name, func(t *testing.T) {
			fn := sc.fn
			fnRecover := func() (skipped bool) {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s panicked: %v", sc.name, r)
					}
				}()
				fn(t, fx, Options{})
				return t.Skipped()
			}
			if !fnRecover() {
				t.Errorf("%s should have skipped on a non-multi fixture", sc.name)
			}
		})
	}
}

// singleFixture is a Fixture-only fake used to confirm the cross-plugin
// scenarios skip when no MultiPluginFixture is provided.
type singleFixture struct{}

func (singleFixture) Plugin() mcpplugin.Plugin                        { return nil }
func (singleFixture) NewAuthContext(*testing.T) mcpplugin.AuthContext { return mcpplugin.AuthContext{} }
func (singleFixture) NewProject(*testing.T) string                    { return "p" }
func (singleFixture) HasStorage() bool                                { return true }
func (singleFixture) Cleanup(*testing.T)                              {}
