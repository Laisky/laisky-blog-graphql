package pageindex

import (
	"context"
	"strings"
)

// runMarkdown builds a header-driven tree and optionally summarizes leaves.
func (idx *Indexer) runMarkdown(ctx context.Context, data []byte, rep *Reporter, stats *Stats) (*Tree, error) {
	rep.Report(Progress{Phase: "md:headers", Percent: 10})
	headers, err := ExtractHeaders(data)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	rep.Report(Progress{Phase: "md:tree", Percent: 30})
	roots := BuildMarkdownTree(headers, lines)
	if len(roots) == 0 {
		// Synthesize a single root node when the document carries no headings.
		roots = []*Node{{Title: "Document", LineNum: 1, Text: string(data)}}
	}
	assignNodeIDs(roots, 0)

	pages := buildMarkdownPages(roots)
	tree := &Tree{
		LineCount: len(lines),
		Structure: roots,
		Pages:     pages,
	}
	if idx.cfg.Algo.GenerateNodeSummary {
		rep.Report(Progress{Phase: "md:summarize", Percent: 70})
		budget := NewBudget(int64(idx.cfg.Algo.MaxTokenNumEachNode * (len(roots) + 1)))
		if err := idx.summarizeMarkdownNodes(ctx, roots, budget, stats); err != nil {
			return nil, err
		}
	}
	return tree, nil
}

func buildMarkdownPages(nodes []*Node) []Page {
	pages := []Page{}
	WalkNodes(nodes, func(n *Node) {
		pages = append(pages, Page{Page: n.LineNum, LineNum: n.LineNum, Content: n.Text})
	})
	return pages
}

func (idx *Indexer) summarizeMarkdownNodes(ctx context.Context, nodes []*Node, budget *Budget, stats *Stats) error {
	var visit func(n *Node) error
	visit = func(n *Node) error {
		if len(n.Children) == 0 {
			prompt, err := RenderPrompt(PromptGenerateNodeSummary, GenerateNodeSummaryVars{Text: n.Text})
			if err != nil {
				return err
			}
			resp, err := idx.callLLM(ctx, Request{Input: userInput(prompt)}, budget, stats)
			if err != nil {
				if err == ErrBudgetExceeded {
					return nil
				}
				return err
			}
			n.Summary = strings.TrimSpace(resp.Text)
		}
		for _, c := range n.Children {
			if err := visit(c); err != nil {
				return err
			}
		}
		return nil
	}
	for _, n := range nodes {
		if err := visit(n); err != nil {
			return err
		}
	}
	return nil
}
