package pageindex

import (
	"context"
	"strings"
	"testing"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

func TestPluginInterfaceAssertion(_ *testing.T) {
	var _ mcpplugin.Plugin = (*Plugin)(nil)
}

func TestPluginCapabilities(t *testing.T) {
	p := &Plugin{}
	caps := p.Capabilities()
	if len(caps.SearchModes) != 1 || caps.SearchModes[0] != mcpplugin.SearchModeTreeReasoning {
		t.Fatalf("expected single tree-reasoning mode, got %+v", caps.SearchModes)
	}
	if !caps.SupportsRandomIO || !caps.SupportsRename || caps.SupportsVersions || caps.AsyncIndexing {
		t.Fatalf("unexpected caps: %+v", caps)
	}
}

func TestPluginStartRefusesWhenDisabled(t *testing.T) {
	p, err := New(PluginDeps{UserFS: &files.Service{}, SystemFS: newMemoryFS(), Settings: Settings{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err == nil {
		t.Fatal("expected Start to fail when api_key empty")
	}
}

func TestPluginRejectOverwriteOffsetOnPDF(t *testing.T) {
	p, err := New(PluginDeps{UserFS: &files.Service{}, SystemFS: newMemoryFS(), Settings: Settings{LLM: LLMSettings{APIKey: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Write(context.Background(), files.AuthContext{}, "proj", "doc.pdf", "...", "utf8", 10, files.WriteModeOverwrite)
	if err == nil || !strings.Contains(err.Error(), "INVALID_ARGUMENT") {
		t.Fatalf("expected INVALID_ARGUMENT, got %v", err)
	}
}

// TestPluginRejectOverwriteOffsetOnMD pins the C09b/P06 contract for the .md
// long-doc path: OVERWRITE@offset>0 must fail with INVALID_ARGUMENT just as
// it does for .pdf files.
func TestPluginRejectOverwriteOffsetOnMD(t *testing.T) {
	p, err := New(PluginDeps{UserFS: &files.Service{}, SystemFS: newMemoryFS(), Settings: Settings{LLM: LLMSettings{APIKey: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Write(context.Background(), files.AuthContext{}, "proj", "notes.md", "...", "utf8", 4, files.WriteModeOverwrite)
	if err == nil || !strings.Contains(err.Error(), "INVALID_ARGUMENT") {
		t.Fatalf("expected INVALID_ARGUMENT for .md OVERWRITE@offset, got %v", err)
	}
}

// TestIsLongDocPathClassification documents the path-suffix gate that wraps
// every long-doc-only check (P06/P13/P18 share this gate).
func TestIsLongDocPathClassification(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/notes/x.pdf", true},
		{"/Notes/X.PDF", true},
		{"/notes/y.md", true},
		{"/notes/Z.MD", true},
		{"/notes/y.txt", false},
		{"/notes/y", false},
	}
	for _, c := range cases {
		if got := isLongDocPath(c.path); got != c.want {
			t.Errorf("isLongDocPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
