package pageindex

import (
	"context"
	"strings"

	errors "github.com/Laisky/errors/v2"
	glog "github.com/Laisky/zap"
)

// runMarkdown builds a header-driven tree and optionally summarizes leaves.
//
// The tree holds at most Limits.MaxNodesPerDocument nodes: headings past the cap
// stay in the last section's text, so no content is dropped. Summaries share one
// operation budget whose call and token ceilings are absolute; the per-node token
// allowance of earlier releases still applies when it is smaller, so small
// documents spend exactly as before. Leaves the budget cannot pay for get a
// deterministic local summary instead of failing the write.
func (idx *Indexer) runMarkdown(ctx context.Context, data []byte, tenant string, rep *Reporter, stats *Stats) (*Tree, error) {
	limits := idx.cfg.Limits.WithDefaults()
	rep.Report(Progress{Phase: "md:headers", Percent: 10})
	extracted, err := extractMarkdownHeadings(ctx, data, limits.MaxNodesPerDocument)
	if err != nil {
		return nil, err
	}
	if extracted.truncated {
		stats.markTruncated()
	}
	if idx.log != nil && (extracted.truncated || extracted.fallback) {
		idx.log.Debug("pageindex.markdown.structural_limit",
			glog.Bool("truncated", extracted.truncated),
			glog.Bool("fallback_scanner", extracted.fallback),
			glog.Int("headings", len(extracted.headers)),
		)
	}
	lines := strings.Split(string(data), "\n")
	if err := ctx.Err(); err != nil {
		return nil, errors.WithStack(err)
	}
	rep.Report(Progress{Phase: "md:tree", Percent: 30})
	roots := BuildMarkdownTree(extracted.headers, lines)
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
		allowance := int64(idx.cfg.Algo.MaxTokenNumEachNode) * int64(len(roots)+1)
		budget := newOperationBudget(limits.documentTokenCeiling(allowance), limits.MaxLLMCallsPerDocument, unlimited, tenant)
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

// summarizeMarkdownNodes summarizes every leaf in document order. A leaf whose
// call the budget refuses gets localNodeSummary; any other error aborts.
func (idx *Indexer) summarizeMarkdownNodes(ctx context.Context, nodes []*Node, budget *Budget, stats *Stats) error {
	var visit func(n *Node) error
	visit = func(n *Node) error {
		if len(n.Children) == 0 {
			summary, err := idx.summarizeLeaf(ctx, n.Text, budget, stats)
			if err != nil {
				return err
			}
			n.Summary = summary
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

// summarizeLeaf returns the LLM summary of text, or its local summary when the
// budget refuses the call or the indexing deadline (indexer.timeout_index) has
// passed. It checks ctx first so a canceled run stops even when every remaining
// leaf would be summarized locally.
func (idx *Indexer) summarizeLeaf(ctx context.Context, text string, budget *Budget, stats *Stats) (string, error) {
	if err := ctx.Err(); err != nil {
		if !deadlinePassed(ctx) {
			return "", errors.WithStack(err)
		}
		stats.markDeadline()
		stats.addLocalSummary()
		return localNodeSummary(text), nil
	}
	if err := idx.gate.precheck(budget); err != nil {
		stats.addRefusal(err)
		stats.addLocalSummary()
		return localNodeSummary(text), nil
	}
	prompt, err := RenderPrompt(PromptGenerateNodeSummary, GenerateNodeSummaryVars{Text: text})
	if err != nil {
		return "", err
	}
	resp, err := idx.callLLM(ctx, Request{Input: userInput(prompt)}, budget, stats)
	if err != nil {
		if errors.Is(err, ErrBudgetExceeded) || deadlinePassed(ctx) {
			if deadlinePassed(ctx) {
				stats.markDeadline()
			}
			stats.addLocalSummary()
			return localNodeSummary(text), nil
		}
		return "", err
	}
	return strings.TrimSpace(resp.Text), nil
}

// deadlinePassed reports that ctx ended because its deadline passed, which the
// pipelines treat like an exhausted budget, as opposed to a cancellation, which
// aborts the run.
func deadlinePassed(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.DeadlineExceeded)
}
