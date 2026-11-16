package pageindex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	errors "github.com/Laisky/errors/v2"
	glog "github.com/Laisky/zap"
)

// maxExpansionDepth bounds phase-8 recursion (proposal §2.6.4.1: ≤3).
const maxExpansionDepth = 3

// expandLargeNodes walks the assembled tree and re-runs no-TOC TOC detection
// on any leaf whose page span and token weight both exceed the configured
// thresholds. Recursion is bounded by maxExpansionDepth so pathological
// inputs cannot loop forever. Mirrors process_large_node_recursively.
func (idx *Indexer) expandLargeNodes(ctx context.Context, nodes []*Node, pages []string, depth int, budget *Budget, stats *Stats) error {
	if depth >= maxExpansionDepth {
		return nil
	}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if budget != nil && budget.Remaining() <= 0 {
			return nil
		}
		// Recurse into existing children first; they may themselves be large.
		if len(n.Children) > 0 {
			if err := idx.expandLargeNodes(ctx, n.Children, pages, depth+1, budget, stats); err != nil {
				return err
			}
			continue
		}
		if !idx.shouldExpand(n, pages) {
			continue
		}
		if err := idx.expandNode(ctx, n, pages, depth, budget, stats); err != nil {
			if errors.Is(err, ErrBudgetExceeded) {
				return nil
			}
			if idx.log != nil {
				idx.log.Warn("pageindex.expand.failed",
					glog.String("title", n.Title),
					glog.Int("start_index", n.StartIndex),
					glog.Int("end_index", n.EndIndex),
					glog.Error(err),
				)
			}
			// Leave this node as a leaf; continue with siblings.
			continue
		}
		if len(n.Children) > 0 {
			if err := idx.expandLargeNodes(ctx, n.Children, pages, depth+1, budget, stats); err != nil {
				return err
			}
		}
	}
	return nil
}

// shouldExpand reports whether node n exceeds both the page-count and token
// thresholds (matches the AND clause in process_large_node_recursively).
func (idx *Indexer) shouldExpand(n *Node, pages []string) bool {
	if n == nil {
		return false
	}
	start, end := n.StartIndex, n.EndIndex
	if start <= 0 || end < start || end > len(pages) {
		return false
	}
	pageSpan := end - start
	if pageSpan <= idx.cfg.Algo.MaxPageNumEachNode {
		return false
	}
	maxTokens := idx.cfg.Algo.MaxTokenNumEachNode
	if maxTokens <= 0 {
		return false
	}
	// Sum tokens across the slice; tokenizer is cheap relative to LLM calls.
	tokens := 0
	for i := start - 1; i < end && i < len(pages); i++ {
		tokens += idx.tok.Count(pages[i])
		if tokens >= maxTokens {
			return true
		}
	}
	return tokens >= maxTokens
}

// expandNode re-runs no-TOC generation on the node's slice and replaces the
// leaf with the resulting children. Errors leave the node untouched.
func (idx *Indexer) expandNode(ctx context.Context, n *Node, pages []string, depth int, budget *Budget, stats *Stats) error {
	if n == nil {
		return nil
	}
	slice, base := nodePageSlice(n, pages)
	if len(slice) == 0 {
		return nil
	}
	flat, err := idx.generateNoTOC(ctx, slice, base, budget, stats)
	if err != nil {
		return err
	}
	if len(flat) == 0 {
		return nil
	}
	children := expansionChildren(n, assembleSubtree(flat, n.EndIndex))
	if len(children) == 0 {
		return nil
	}
	// The nodes actually inserted must fit the document's node ceiling;
	// otherwise the node stays a leaf and keeps covering its whole page range.
	if !budget.takeNodes(countNodes(children)) {
		stats.markTruncated()
		return nil
	}
	// The parent keeps only the pages before its first child.
	n.EndIndex = max(children[0].StartIndex-1, n.StartIndex)
	n.Children = children
	if idx.log != nil {
		idx.log.Debug("pageindex.expand.node",
			glog.String("title", n.Title),
			glog.Int("depth", depth),
			glog.Int("children", len(n.Children)),
		)
	}
	return nil
}

// expansionChildren returns the forest an expansion of n inserts under it. When
// the first generated section repeats n's own title, that section is dropped and
// the sections nested under it are promoted, as the upstream algorithm
// (process_large_node_recursively) drops only the repeated entry before it
// assembles the tree. The node ceiling is charged for exactly this forest.
func expansionChildren(n *Node, subNodes []*Node) []*Node {
	if len(subNodes) == 0 || subNodes[0] == nil || subNodes[0].Title != strings.TrimSpace(n.Title) {
		return subNodes
	}
	promoted := make([]*Node, 0, len(subNodes[0].Children)+len(subNodes)-1)
	promoted = append(promoted, subNodes[0].Children...)
	return append(promoted, subNodes[1:]...)
}

// countNodes returns the number of nodes in the forest rooted at nodes.
func countNodes(nodes []*Node) int {
	count := 0
	WalkNodes(nodes, func(*Node) { count++ })
	return count
}

// nodePageSlice returns pages[StartIndex-1:EndIndex] alongside the absolute
// page number of slice[0] so the no-TOC prompt can emit physical_index tags
// in absolute coordinates.
func nodePageSlice(n *Node, pages []string) ([]string, int) {
	start, end := n.StartIndex, n.EndIndex
	if start < 1 {
		start = 1
	}
	if end > len(pages) {
		end = len(pages)
	}
	if end < start {
		return nil, start
	}
	out := make([]string, 0, end-start+1)
	for i := start - 1; i < end; i++ {
		out = append(out, pages[i])
	}
	return out, start
}

// generateNoTOC is the slice-scoped equivalent of modeCNoTOC. base is the
// absolute physical_index of slice[0]; emitted tags use absolute coords so
// the existing physicalIndexInt parser yields document-global pages.
func (idx *Indexer) generateNoTOC(ctx context.Context, slice []string, base int, budget *Budget, stats *Stats) ([]map[string]any, error) {
	if len(slice) == 0 {
		return nil, nil
	}
	groups := groupPagesWithBase(slice, base, idx.tok, idx.cfg.Algo.MaxTokenNumEachNode)
	if len(groups) == 0 {
		return nil, nil
	}
	initPrompt, err := RenderPrompt(PromptGenerateTOCInit, GenerateTOCInitVars{Part: groups[0]})
	if err != nil {
		return nil, err
	}
	resp, err := idx.callLLM(ctx, tocRequest(initPrompt), budget, stats)
	if err != nil {
		return nil, err
	}
	flat, err := parseTOCList(resp.Text)
	if err != nil {
		return nil, err
	}
	for _, g := range groups[1:] {
		if budget != nil && budget.Remaining() <= 0 {
			break
		}
		prevJSON, err := json.Marshal(flat)
		if err != nil {
			return nil, errors.Wrap(err, "encode index structure")
		}
		contPrompt, err := RenderPrompt(PromptGenerateTOCContinue, GenerateTOCContinueVars{Part: g, PreviousStruct: string(prevJSON)})
		if err != nil {
			return nil, err
		}
		respC, err := idx.callLLM(ctx, tocRequest(contPrompt), budget, stats)
		if err != nil {
			if errors.Is(err, ErrBudgetExceeded) {
				break
			}
			return nil, err
		}
		extra, err := parseTOCList(respC.Text)
		if err != nil {
			continue
		}
		flat = append(flat, extra...)
	}
	return flat, nil
}

// groupPagesWithBase splits pages into prompt groups of at most maxTokens tokens
// each (a page larger than that is a group of its own), tagging every page with
// its physical_index in absolute coordinates: pages[0] is page base.
func groupPagesWithBase(pages []string, base int, tok Tokenizer, maxTokens int) []string {
	if maxTokens <= 0 {
		maxTokens = 20000
	}
	wrapped := make([]string, 0, len(pages))
	for i, p := range pages {
		page := base + i
		wrapped = append(wrapped, fmt.Sprintf("<physical_index_%d>\n%s\n<physical_index_%d>\n\n", page, p, page))
	}
	groups := []string{}
	var cur strings.Builder
	curTokens := 0
	for _, p := range wrapped {
		t := tok.Count(p)
		if curTokens+t > maxTokens && cur.Len() > 0 {
			groups = append(groups, cur.String())
			cur.Reset()
			curTokens = 0
		}
		cur.WriteString(p)
		curTokens += t
	}
	if cur.Len() > 0 {
		groups = append(groups, cur.String())
	}
	return groups
}

// assembleSubtree builds child nodes from the flat list produced by
// generateNoTOC. It uses the existing parentStructure helper to reattach
// nested entries; the parent's EndIndex is the upper bound for the trailing
// node.
func assembleSubtree(flat []map[string]any, parentEnd int) []*Node {
	type entry struct {
		structure string
		node      *Node
	}
	entries := make([]entry, 0, len(flat))
	for _, item := range flat {
		title, ok := tocEntryTitle(item)
		if !ok {
			continue
		}
		structure, _ := item["structure"].(string)
		startIdx := 0
		if v, ok := item["physical_index"]; ok {
			if n, ok := physicalIndexInt(v); ok {
				startIdx = n
			}
		}
		if startIdx <= 0 {
			continue
		}
		entries = append(entries, entry{structure: structure, node: &Node{Title: title, StartIndex: startIdx}})
	}
	if len(entries) == 0 {
		return nil
	}
	for i, e := range entries {
		if i+1 < len(entries) {
			e.node.EndIndex = max(entries[i+1].node.StartIndex-1, e.node.StartIndex)
		} else {
			e.node.EndIndex = parentEnd
		}
	}
	byStructure := map[string]*Node{}
	for _, e := range entries {
		if e.structure != "" {
			byStructure[e.structure] = e.node
		}
	}
	roots := []*Node{}
	for _, e := range entries {
		parent := parentStructure(e.structure)
		if parent == "" {
			roots = append(roots, e.node)
			continue
		}
		if p, ok := byStructure[parent]; ok {
			p.Children = append(p.Children, e.node)
		} else {
			roots = append(roots, e.node)
		}
	}
	return roots
}
