package pageindex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	errors "github.com/Laisky/errors/v2"
)

// runPDF executes the §2.6.4.1 nine-phase PDF pipeline.
func (idx *Indexer) runPDF(ctx context.Context, data []byte, rep *Reporter, stats *Stats) (*Tree, error) {
	rep.Report(Progress{Phase: "pdf:extract", Percent: 5})
	pages, err := idx.pdf.PagesText(ctx, data)
	if err != nil {
		return nil, errors.Wrap(err, "extract pages")
	}
	if len(pages) == 0 {
		return nil, errors.New("pdf has zero pages")
	}
	pageCount := len(pages)
	budget := NewBudget(int64(idx.cfg.Algo.MaxTokenNumEachNode*pageCount + idx.cfg.TreeQuery.MaxTokens))

	rep.Report(Progress{Phase: "pdf:toc-detect", Percent: 15})
	tocPages, tocContent, hasPageIndex, err := idx.detectTOC(ctx, pages, budget, stats)
	if err != nil {
		return nil, err
	}

	rep.Report(Progress{Phase: "pdf:tree-build", Percent: 40})
	flat, err := idx.buildFlatTOC(ctx, pages, tocPages, tocContent, hasPageIndex, budget, stats)
	if err != nil {
		return nil, err
	}
	if len(flat) == 0 {
		// Fallback: emit a single "Document" node spanning all pages.
		flat = []map[string]any{{"structure": "1", "title": "Document", "physical_index": 1}}
	}

	rep.Report(Progress{Phase: "pdf:tree-assemble", Percent: 65})
	rootNodes := assembleTree(flat, pageCount)

	if idx.cfg.Algo.GenerateNodeSummary {
		rep.Report(Progress{Phase: "pdf:summarize", Percent: 75})
		if err := idx.summarizeNodes(ctx, rootNodes, pages, budget, stats); err != nil {
			return nil, err
		}
	}

	// Phase 7: title-appearance verification + targeted fix.
	rep.Report(Progress{Phase: "pdf:verify", Percent: 80})
	tmpTree := &Tree{Structure: rootNodes}
	if err := idx.verifyAndFix(ctx, tmpTree, pages, budget, stats); err != nil {
		return nil, errors.Wrap(err, "verify and fix")
	}

	// Phase 8: bounded recursive expansion of oversized leaf nodes.
	rep.Report(Progress{Phase: "pdf:expand", Percent: 88})
	if err := idx.expandLargeNodes(ctx, rootNodes, pages, 0, budget, stats); err != nil {
		return nil, errors.Wrap(err, "expand large nodes")
	}
	// Re-stamp node IDs so newly inserted children get stable identifiers.
	assignNodeIDs(rootNodes, 0)

	docDescription := ""
	if idx.cfg.Algo.GenerateDocDescription {
		rep.Report(Progress{Phase: "pdf:doc-description", Percent: 95})
		desc, err := idx.generateDocDescription(ctx, rootNodes, budget, stats)
		if err == nil {
			docDescription = desc
		}
	}
	tree := &Tree{
		PageCount:      pageCount,
		Structure:      rootNodes,
		DocDescription: docDescription,
		Pages:          buildPageCache(pages),
	}
	return tree, nil
}

func buildPageCache(pages []string) []Page {
	out := make([]Page, 0, len(pages))
	for i, t := range pages {
		out = append(out, Page{Page: i + 1, Content: t})
	}
	return out
}

// detectTOC mirrors phases 2 & 3.
func (idx *Indexer) detectTOC(ctx context.Context, pages []string, budget *Budget, stats *Stats) ([]int, string, bool, error) {
	maxCheck := idx.cfg.Algo.TocCheckPageNum
	if maxCheck > len(pages) {
		maxCheck = len(pages)
	}
	tocPages := make([]int, 0, maxCheck)
	lastYes := false
	for i := 0; i < len(pages); i++ {
		if i >= maxCheck && !lastYes {
			break
		}
		text, err := RenderPrompt(PromptTOCDetectorSinglePage, TOCDetectorSinglePageVars{Content: pages[i]})
		if err != nil {
			return nil, "", false, err
		}
		req := Request{Input: userInput(text)}
		resp, err := idx.callLLM(ctx, req, budget, stats)
		if err != nil {
			return nil, "", false, err
		}
		ans := parseSimpleAnswer(resp.Text, "toc_detected")
		if ans == "yes" {
			tocPages = append(tocPages, i)
			lastYes = true
			continue
		}
		if lastYes {
			break
		}
	}
	if len(tocPages) == 0 {
		return nil, "", false, nil
	}
	var sb strings.Builder
	for _, p := range tocPages {
		sb.WriteString(pages[p])
	}
	tocContent := transformDotsToColon(sb.String())
	text, err := RenderPrompt(PromptDetectPageIndex, DetectPageIndexVars{TOCContent: tocContent})
	if err != nil {
		return nil, "", false, err
	}
	resp, err := idx.callLLM(ctx, Request{Input: userInput(text)}, budget, stats)
	if err != nil {
		return nil, "", false, err
	}
	hasPageIndex := parseSimpleAnswer(resp.Text, "page_index_given_in_toc") == "yes"
	return tocPages, tocContent, hasPageIndex, nil
}

func transformDotsToColon(in string) string {
	out := in
	out = strings.ReplaceAll(out, "....", ": ")
	out = strings.ReplaceAll(out, "...", ": ")
	return out
}

// buildFlatTOC dispatches to mode A/B/C parity with upstream meta_processor.
func (idx *Indexer) buildFlatTOC(ctx context.Context, pages []string, tocPages []int, tocContent string, hasPageIndex bool, budget *Budget, stats *Stats) ([]map[string]any, error) {
	switch {
	case len(tocPages) > 0 && hasPageIndex:
		return idx.modeAWithPageNumbers(ctx, pages, tocPages, tocContent, budget, stats)
	case len(tocPages) > 0:
		return idx.modeBNoPageNumbers(ctx, pages, tocContent, budget, stats)
	default:
		return idx.modeCNoTOC(ctx, pages, budget, stats)
	}
}

// modeAWithPageNumbers — toc_transformer + toc_index_extractor.
func (idx *Indexer) modeAWithPageNumbers(ctx context.Context, pages []string, tocPages []int, tocContent string, budget *Budget, stats *Stats) ([]map[string]any, error) {
	transformerPrompt, err := RenderPrompt(PromptTOCTransformer, TOCTransformerVars{TOCContent: tocContent})
	if err != nil {
		return nil, err
	}
	resp, err := idx.callLLM(ctx, Request{Input: userInput(transformerPrompt)}, budget, stats)
	if err != nil {
		return nil, err
	}
	flat, err := parseTOCList(resp.Text)
	if err != nil {
		return nil, err
	}
	startPage := tocPages[len(tocPages)-1] + 1
	end := startPage + idx.cfg.Algo.TocCheckPageNum
	if end > len(pages) {
		end = len(pages)
	}
	var content strings.Builder
	for p := startPage; p < end; p++ {
		content.WriteString(fmt.Sprintf("<physical_index_%d>\n%s\n<physical_index_%d>\n\n", p+1, pages[p], p+1))
	}
	tocJSON, _ := json.Marshal(flat)
	extractorPrompt, err := RenderPrompt(PromptTOCIndexExtractor, TOCIndexExtractorVars{TOC: string(tocJSON), Content: content.String()})
	if err != nil {
		return nil, err
	}
	resp2, err := idx.callLLM(ctx, Request{Input: userInput(extractorPrompt)}, budget, stats)
	if err != nil {
		return nil, err
	}
	extracted, err := parseTOCList(resp2.Text)
	if err != nil {
		return flat, nil
	}
	mergeIndices(flat, extracted)
	return flat, nil
}

func mergeIndices(dst, src []map[string]any) {
	idx := map[string]map[string]any{}
	for _, e := range src {
		if t, ok := e["title"].(string); ok {
			idx[t] = e
		}
	}
	for _, item := range dst {
		title, _ := item["title"].(string)
		if e, ok := idx[title]; ok {
			if v, ok := e["physical_index"]; ok {
				if n, ok := physicalIndexInt(v); ok {
					item["physical_index"] = n
				}
			}
		}
	}
}

// modeBNoPageNumbers — toc_transformer + add_page_number_to_toc per group.
func (idx *Indexer) modeBNoPageNumbers(ctx context.Context, pages []string, tocContent string, budget *Budget, stats *Stats) ([]map[string]any, error) {
	transformerPrompt, err := RenderPrompt(PromptTOCTransformer, TOCTransformerVars{TOCContent: tocContent})
	if err != nil {
		return nil, err
	}
	resp, err := idx.callLLM(ctx, Request{Input: userInput(transformerPrompt)}, budget, stats)
	if err != nil {
		return nil, err
	}
	flat, err := parseTOCList(resp.Text)
	if err != nil {
		return nil, err
	}
	groups := groupPagesByTokens(pages, idx.tok, idx.cfg.Algo.MaxTokenNumEachNode)
	for _, g := range groups {
		structJSON, _ := json.Marshal(flat)
		filler, err := RenderPrompt(PromptAddPageNumberToTOC, AddPageNumberToTOCVars{Part: g, Structure: string(structJSON)})
		if err != nil {
			return nil, err
		}
		respFill, err := idx.callLLM(ctx, Request{Input: userInput(filler)}, budget, stats)
		if err != nil {
			return nil, err
		}
		next, err := parseTOCList(respFill.Text)
		if err == nil && len(next) > 0 {
			mergeIndices(flat, next)
		}
	}
	return flat, nil
}

// modeCNoTOC — generate_toc_init + generate_toc_continue.
func (idx *Indexer) modeCNoTOC(ctx context.Context, pages []string, budget *Budget, stats *Stats) ([]map[string]any, error) {
	groups := groupPagesByTokens(pages, idx.tok, idx.cfg.Algo.MaxTokenNumEachNode)
	if len(groups) == 0 {
		return nil, nil
	}
	initPrompt, err := RenderPrompt(PromptGenerateTOCInit, GenerateTOCInitVars{Part: groups[0]})
	if err != nil {
		return nil, err
	}
	resp, err := idx.callLLM(ctx, Request{Input: userInput(initPrompt)}, budget, stats)
	if err != nil {
		return nil, err
	}
	flat, err := parseTOCList(resp.Text)
	if err != nil {
		return nil, err
	}
	for _, g := range groups[1:] {
		prevJSON, _ := json.Marshal(flat)
		contPrompt, err := RenderPrompt(PromptGenerateTOCContinue, GenerateTOCContinueVars{Part: g, PreviousStruct: string(prevJSON)})
		if err != nil {
			return nil, err
		}
		respC, err := idx.callLLM(ctx, Request{Input: userInput(contPrompt)}, budget, stats)
		if err != nil {
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

func groupPagesByTokens(pages []string, tok Tokenizer, maxTokens int) []string {
	if maxTokens <= 0 {
		maxTokens = 20000
	}
	wrapped := make([]string, 0, len(pages))
	for i, p := range pages {
		wrapped = append(wrapped, fmt.Sprintf("<physical_index_%d>\n%s\n<physical_index_%d>\n\n", i+1, p, i+1))
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

// assembleTree converts a flat list with structure indices into a tree.
func assembleTree(flat []map[string]any, totalPages int) []*Node {
	type entry struct {
		structure string
		node      *Node
	}
	entries := make([]entry, 0, len(flat))
	for _, item := range flat {
		title, _ := item["title"].(string)
		if title == "" {
			continue
		}
		structure, _ := item["structure"].(string)
		startIdx := 0
		if v, ok := item["physical_index"]; ok {
			if n, ok := physicalIndexInt(v); ok {
				startIdx = n
			}
		}
		if startIdx == 0 {
			startIdx = 1
		}
		entries = append(entries, entry{structure: structure, node: &Node{Title: strings.TrimSpace(title), StartIndex: startIdx}})
	}
	for i, e := range entries {
		if i+1 < len(entries) {
			e.node.EndIndex = max(entries[i+1].node.StartIndex-1, e.node.StartIndex)
		} else {
			e.node.EndIndex = totalPages
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
	assignNodeIDs(roots, 0)
	return roots
}

func parentStructure(s string) string {
	if s == "" {
		return ""
	}
	parts := strings.Split(s, ".")
	if len(parts) <= 1 {
		return ""
	}
	return strings.Join(parts[:len(parts)-1], ".")
}

func assignNodeIDs(nodes []*Node, counter int) int {
	for _, n := range nodes {
		counter++
		n.NodeID = fmt.Sprintf("%04d", counter)
		counter = assignNodeIDs(n.Children, counter)
	}
	return counter
}

// summarizeNodes runs PromptGenerateNodeSummary on every leaf node sequentially.
func (idx *Indexer) summarizeNodes(ctx context.Context, nodes []*Node, pages []string, budget *Budget, stats *Stats) error {
	var visit func(n *Node) error
	visit = func(n *Node) error {
		if len(n.Children) == 0 {
			text := pageRangeText(pages, n.StartIndex, n.EndIndex)
			prompt, err := RenderPrompt(PromptGenerateNodeSummary, GenerateNodeSummaryVars{Text: text})
			if err != nil {
				return err
			}
			resp, err := idx.callLLM(ctx, Request{Input: userInput(prompt)}, budget, stats)
			if err != nil {
				if errors.Is(err, ErrBudgetExceeded) {
					return nil
				}
				return err
			}
			n.Summary = strings.TrimSpace(resp.Text)
			return nil
		}
		for _, child := range n.Children {
			if err := visit(child); err != nil {
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

func pageRangeText(pages []string, start, end int) string {
	if start < 1 {
		start = 1
	}
	if end > len(pages) || end <= 0 {
		end = len(pages)
	}
	var sb strings.Builder
	for i := start - 1; i < end && i < len(pages); i++ {
		sb.WriteString(pages[i])
	}
	return sb.String()
}

// generateDocDescription wraps the doc-description prompt.
func (idx *Indexer) generateDocDescription(ctx context.Context, nodes []*Node, budget *Budget, stats *Stats) (string, error) {
	outline, _ := json.Marshal(CloneOutline(nodes))
	prompt, err := RenderPrompt(PromptGenerateDocDescription, GenerateDocDescriptionVars{Structure: string(outline)})
	if err != nil {
		return "", err
	}
	resp, err := idx.callLLM(ctx, Request{Input: userInput(prompt)}, budget, stats)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Text), nil
}
