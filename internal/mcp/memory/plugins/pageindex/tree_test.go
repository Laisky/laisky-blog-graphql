package pageindex

import (
	"encoding/json"
	"testing"
)

func TestTreeRoundTrip(t *testing.T) {
	tree := &Tree{
		DocID:     "abc",
		Type:      KindMarkdown,
		LineCount: 10,
		Structure: []*Node{{
			NodeID: "0001",
			Title:  "Root",
			Children: []*Node{
				{NodeID: "0002", Title: "Child", LineNum: 4, Text: "child body"},
			},
		}},
		Pages: []Page{{Page: 1, LineNum: 1, Content: "x"}},
	}
	body, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Tree
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.DocID != tree.DocID || len(decoded.Structure) != 1 || decoded.Structure[0].Children[0].Title != "Child" {
		t.Fatalf("round-trip mismatch: %+v", decoded)
	}
}

func TestCloneOutlineStripsText(t *testing.T) {
	src := []*Node{{Title: "Root", Text: "body", Children: []*Node{{Title: "Child", Text: "more"}}}}
	out := CloneOutline(src)
	if out[0].Text != "" || out[0].Children[0].Text != "" {
		t.Fatalf("expected stripped text, got %+v", out)
	}
	// Ensure deep copy.
	out[0].Title = "Modified"
	if src[0].Title == "Modified" {
		t.Fatalf("CloneOutline must not alias")
	}
}

func TestWalkNodes(t *testing.T) {
	src := []*Node{{Title: "A", Children: []*Node{{Title: "B"}, {Title: "C"}}}, {Title: "D"}}
	var titles []string
	WalkNodes(src, func(n *Node) { titles = append(titles, n.Title) })
	if len(titles) != 4 {
		t.Fatalf("expected 4 titles, got %v", titles)
	}
}
