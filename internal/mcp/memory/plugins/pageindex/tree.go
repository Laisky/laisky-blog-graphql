package pageindex

// Tree is the persisted PageIndex document structure.
type Tree struct {
	DocID           string  `json:"doc_id"`
	DocName         string  `json:"doc_name,omitempty"`
	DocDescription  string  `json:"doc_description,omitempty"`
	Type            DocKind `json:"type"`
	PageCount       int     `json:"page_count,omitempty"`
	LineCount       int     `json:"line_count,omitempty"`
	IndexedAt       string  `json:"indexed_at,omitempty"`
	AlgorithmVer    string  `json:"algorithm_version"`
	Structure       []*Node `json:"structure"`
	// Pages caches per-page extracted text so retrieval can resolve page ranges
	// without re-parsing the original bytes.
	Pages []Page `json:"pages,omitempty"`
}

// Node is a hierarchical PageIndex tree node (parity with upstream JSON shape).
type Node struct {
	NodeID     string  `json:"node_id,omitempty"`
	Title      string  `json:"title"`
	StartIndex int     `json:"start_index,omitempty"`
	EndIndex   int     `json:"end_index,omitempty"`
	LineNum    int     `json:"line_num,omitempty"`
	Summary    string  `json:"summary,omitempty"`
	Text       string  `json:"text,omitempty"`
	Children   []*Node `json:"nodes,omitempty"`
}

// Page is one page (PDF) or one heading section (Markdown).
type Page struct {
	Page    int    `json:"page"`
	Content string `json:"content"`
	LineNum int    `json:"line_num,omitempty"`
}

// Chunk is the retrieval response unit produced by GetPageContent.
type Chunk struct {
	DocID    string  `json:"doc_id"`
	FilePath string  `json:"file_path"`
	Page     int     `json:"page"`
	LineNum  int     `json:"line_num,omitempty"`
	Content  string  `json:"content"`
	Score    float64 `json:"score"`
}

// PageRange is a half-open range of page indices [Start,End].
type PageRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// StructureView is a token-light view returned by GetDocumentStructure.
type StructureView struct {
	DocID          string  `json:"doc_id"`
	DocName        string  `json:"doc_name,omitempty"`
	DocDescription string  `json:"doc_description,omitempty"`
	PageCount      int     `json:"page_count,omitempty"`
	LineCount      int     `json:"line_count,omitempty"`
	Outline        []*Node `json:"outline"`
}

// DocKind discriminates supported source documents.
type DocKind string

const (
	// KindPDF identifies a PDF source.
	KindPDF DocKind = "pdf"
	// KindMarkdown identifies a markdown source.
	KindMarkdown DocKind = "md"
)

// CloneOutline returns a deep copy of nodes with the heavy Text field stripped.
func CloneOutline(nodes []*Node) []*Node {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]*Node, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, cloneNodeOutline(n))
	}
	return out
}

func cloneNodeOutline(n *Node) *Node {
	if n == nil {
		return nil
	}
	cp := *n
	cp.Text = ""
	cp.Children = CloneOutline(n.Children)
	return &cp
}

// WalkNodes visits every node in pre-order.
func WalkNodes(nodes []*Node, visit func(*Node)) {
	for _, n := range nodes {
		if n == nil {
			continue
		}
		visit(n)
		WalkNodes(n.Children, visit)
	}
}
