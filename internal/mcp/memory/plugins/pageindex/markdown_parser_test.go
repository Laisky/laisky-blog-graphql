package pageindex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractHeaders(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "sample.md"))
	if err != nil {
		t.Fatal(err)
	}
	headers, err := ExtractHeaders(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(headers) < 4 {
		t.Fatalf("expected at least 4 headers, got %d", len(headers))
	}
	if headers[0].Title != "Sample Document" {
		t.Fatalf("expected first header to be 'Sample Document', got %q", headers[0].Title)
	}
}

func TestBuildMarkdownTree(t *testing.T) {
	body := []byte("# A\n\ntext\n\n## B\n\nbody\n\n## C\n\nbody c\n")
	headers, err := ExtractHeaders(body)
	if err != nil {
		t.Fatal(err)
	}
	lines := splitLines(body)
	tree := BuildMarkdownTree(headers, lines)
	if len(tree) != 1 || len(tree[0].Children) != 2 {
		t.Fatalf("unexpected tree shape: %+v", tree)
	}
}

func splitLines(b []byte) []string {
	out := []string{}
	cur := ""
	for _, c := range string(b) {
		if c == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	out = append(out, cur)
	return out
}
