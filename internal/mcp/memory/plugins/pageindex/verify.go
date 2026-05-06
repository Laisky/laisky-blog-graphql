package pageindex

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"sync"

	errors "github.com/Laisky/errors/v2"
	glog "github.com/Laisky/zap"
	"golang.org/x/sync/errgroup"
)

// titleCheckSampleSize mirrors the upstream verify_toc default N=10 samples.
const titleCheckSampleSize = 10

// titleCheckMaxRetries mirrors fix_incorrect_toc_with_retries' max_attempts=3.
const titleCheckMaxRetries = 3

// titleCheckResult is the JSON shape returned by PromptCheckTitleAppearance.
type titleCheckResult struct {
	Thinking string `json:"thinking,omitempty"`
	Answer   string `json:"answer"`
}

// titleFixerResult is the JSON shape returned by PromptSingleTOCItemIndexFixer.
type titleFixerResult struct {
	Thinking      string `json:"thinking,omitempty"`
	PhysicalIndex any    `json:"physical_index"`
}

// verifyToCSchema is set on the LLM Request so models that honor json_schema
// produce strict yes/no answers.
var verifyToCSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["answer"],
  "properties": {
    "thinking": {"type": "string"},
    "answer": {"type": "string", "enum": ["yes", "no"]}
  }
}`)

// fixerSchema constrains the index fixer LLM output.
var fixerSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["physical_index"],
  "properties": {
    "thinking": {"type": "string"},
    "physical_index": {"type": ["string", "integer"]}
  }
}`)

// verifyAndFix executes phase 7 against the assembled tree: it samples up to
// titleCheckSampleSize nodes, asks the LLM whether each title appears on its
// recorded start page, and — when accuracy falls into the (0.6, 1.0) window —
// re-derives the offending physical_index via PromptSingleTOCItemIndexFixer
// with up to titleCheckMaxRetries retries. accuracy ≤ 0.6 is logged and the
// tree is left untouched (per proposal §2.6.4.1 risk note: full mode
// downgrade is deferred to v1.1).
func (idx *Indexer) verifyAndFix(ctx context.Context, tree *Tree, pages []string, budget *Budget, stats *Stats) error {
	if tree == nil || len(tree.Structure) == 0 {
		return nil
	}
	flat := flattenForVerify(tree.Structure)
	if len(flat) < 2 {
		return nil
	}
	if budget != nil && budget.Remaining() <= 0 {
		return nil
	}
	accuracy, incorrect, err := idx.verifyTOC(ctx, flat, pages, budget, stats)
	if err != nil {
		return errors.Wrap(err, "verify toc")
	}
	if idx.log != nil {
		idx.log.Debug("pageindex.verify.accuracy",
			glog.Float64("accuracy", accuracy),
			glog.Int("incorrect", len(incorrect)),
			glog.Int("checked", len(flat)),
		)
	}
	if len(incorrect) == 0 {
		return nil
	}
	if accuracy <= 0.6 {
		if idx.log != nil {
			idx.log.Warn("pageindex.verify.low_accuracy_skip_fix",
				glog.Float64("accuracy", accuracy),
				glog.Int("incorrect", len(incorrect)),
			)
		}
		return nil
	}
	if accuracy >= 1.0 {
		return nil
	}
	idx.fixIncorrectWithRetries(ctx, flat, pages, incorrect, budget, stats)
	return nil
}

// flatNode pairs a *Node with its index in the pre-order flattened list. The
// upstream Python uses list_index in fix/verify; we replicate that semantics
// because the fixer needs to peek at neighbors to bracket the search range.
type flatNode struct {
	listIndex int
	node      *Node
}

// flattenForVerify returns nodes in pre-order. Only nodes with a non-zero
// StartIndex are eligible; nodes whose start_index is zero are placeholders
// that upstream filters out before verify.
func flattenForVerify(roots []*Node) []flatNode {
	out := make([]flatNode, 0, 16)
	var walk func(n *Node)
	walk = func(n *Node) {
		if n == nil {
			return
		}
		if n.StartIndex > 0 {
			out = append(out, flatNode{listIndex: len(out), node: n})
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return out
}

// incorrectItem mirrors the upstream incorrect_results dict shape with just
// the fields the fixer needs.
type incorrectItem struct {
	listIndex     int
	title         string
	physicalIndex int
}

// verifyTOC samples up to titleCheckSampleSize items concurrently and reports
// accuracy plus the slice of items whose title did not appear on the recorded
// page.
func (idx *Indexer) verifyTOC(ctx context.Context, flat []flatNode, pages []string, budget *Budget, stats *Stats) (float64, []incorrectItem, error) {
	if len(flat) == 0 {
		return 0, nil, nil
	}
	sampleIdx := pickSampleIndices(len(flat), titleCheckSampleSize)
	if len(sampleIdx) == 0 {
		return 0, nil, nil
	}
	type checkOutcome struct {
		listIndex int
		title     string
		page      int
		yes       bool
	}
	results := make([]checkOutcome, len(sampleIdx))

	g, gctx := errgroup.WithContext(ctx)
	for i, si := range sampleIdx {
		i, si := i, si
		fn := flat[si]
		// Skip items lacking a usable physical_index — upstream returns
		// answer='no' for them; we treat them as incorrect with page=0.
		if fn.node.StartIndex <= 0 || fn.node.StartIndex > len(pages) {
			results[i] = checkOutcome{listIndex: fn.listIndex, title: fn.node.Title, page: fn.node.StartIndex, yes: false}
			continue
		}
		g.Go(func() error {
			yes, err := idx.checkTitleAppearance(gctx, fn.node.Title, pages[fn.node.StartIndex-1], budget, stats)
			if err != nil {
				if errors.Is(err, ErrBudgetExceeded) {
					results[i] = checkOutcome{listIndex: fn.listIndex, title: fn.node.Title, page: fn.node.StartIndex, yes: true}
					return nil
				}
				return err
			}
			results[i] = checkOutcome{listIndex: fn.listIndex, title: fn.node.Title, page: fn.node.StartIndex, yes: yes}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return 0, nil, err
	}
	correct := 0
	incorrect := make([]incorrectItem, 0, len(results))
	for _, r := range results {
		if r.yes {
			correct++
			continue
		}
		incorrect = append(incorrect, incorrectItem{listIndex: r.listIndex, title: r.title, physicalIndex: r.page})
	}
	checked := len(results)
	accuracy := 0.0
	if checked > 0 {
		accuracy = float64(correct) / float64(checked)
	}
	return accuracy, incorrect, nil
}

// pickSampleIndices returns up to want unique indices in [0,n). When n ≤ want
// it returns the full deterministic range; otherwise it samples without
// replacement using math/rand (seeded per-call so verify stays reproducible
// for the same input length within a process).
func pickSampleIndices(n, want int) []int {
	if n <= 0 {
		return nil
	}
	if want <= 0 {
		want = n
	}
	if want >= n {
		out := make([]int, n)
		for i := 0; i < n; i++ {
			out[i] = i
		}
		return out
	}
	// Deterministic sampler: shuffle [0,n) using a fresh local rng so test
	// expectations remain stable even though Python's verify_toc uses
	// random.sample. We always sample 'want' positions.
	r := rand.New(rand.NewSource(int64(n*1009 + want*7)))
	perm := r.Perm(n)
	out := make([]int, want)
	copy(out, perm[:want])
	return out
}

// checkTitleAppearance asks the LLM whether title appears (or starts) on
// pageText. Returns true when answer == "yes".
func (idx *Indexer) checkTitleAppearance(ctx context.Context, title, pageText string, budget *Budget, stats *Stats) (bool, error) {
	prompt, err := RenderPrompt(PromptCheckTitleAppearance, CheckTitleAppearanceVars{Title: title, PageText: pageText})
	if err != nil {
		return false, err
	}
	req := Request{
		Input:      userInput(prompt),
		Schema:     verifyToCSchema,
		SchemaName: "title_check",
	}
	resp, err := idx.callLLM(ctx, req, budget, stats)
	if err != nil {
		return false, err
	}
	body := stripCodeFence(resp.Text)
	if body == "" {
		return false, nil
	}
	var out titleCheckResult
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		// Upstream tolerates malformed responses by treating them as 'no'.
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(out.Answer), "yes"), nil
}

// fixIncorrectWithRetries iterates fixIncorrect up to titleCheckMaxRetries
// times. Mirrors fix_incorrect_toc_with_retries.
func (idx *Indexer) fixIncorrectWithRetries(ctx context.Context, flat []flatNode, pages []string, incorrect []incorrectItem, budget *Budget, stats *Stats) {
	current := incorrect
	for attempt := 0; attempt < titleCheckMaxRetries && len(current) > 0; attempt++ {
		if budget != nil && budget.Remaining() <= 0 {
			if idx.log != nil {
				idx.log.Warn("pageindex.fix.budget_exceeded",
					glog.Int("attempt", attempt),
					glog.Int("remaining_invalid", len(current)),
				)
			}
			return
		}
		next := idx.fixIncorrect(ctx, flat, pages, current, budget, stats)
		if len(next) == len(current) && sameIncorrectSet(next, current) {
			// No progress; bail to avoid burning the budget on a hopeless set.
			break
		}
		current = next
	}
	if idx.log != nil && len(current) > 0 {
		idx.log.Warn("pageindex.fix.unresolved",
			glog.Int("remaining_invalid", len(current)),
		)
	}
}

// sameIncorrectSet returns true iff a and b point at the same listIndex set.
func sameIncorrectSet(a, b []incorrectItem) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[int]struct{}, len(a))
	for _, it := range a {
		seen[it.listIndex] = struct{}{}
	}
	for _, it := range b {
		if _, ok := seen[it.listIndex]; !ok {
			return false
		}
	}
	return true
}

// fixIncorrect runs single_toc_item_index_fixer + check_title_appearance per
// incorrect item concurrently, mutates flat[].node.StartIndex in place when
// the new index passes verification, and returns the still-invalid items.
func (idx *Indexer) fixIncorrect(ctx context.Context, flat []flatNode, pages []string, incorrect []incorrectItem, budget *Budget, stats *Stats) []incorrectItem {
	if len(incorrect) == 0 {
		return nil
	}
	incorrectSet := make(map[int]struct{}, len(incorrect))
	for _, it := range incorrect {
		incorrectSet[it.listIndex] = struct{}{}
	}
	type fixResult struct {
		listIndex int
		title     string
		page      int
		valid     bool
	}
	results := make([]fixResult, len(incorrect))

	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	for i, item := range incorrect {
		i, item := i, item
		g.Go(func() error {
			prevPage, nextPage := neighborPages(flat, item.listIndex, incorrectSet, len(pages))
			content := buildPhysicalIndexBlock(pages, prevPage, nextPage)
			fixedPage, ok, err := idx.singleTOCItemIndexFixer(gctx, item.title, content, budget, stats)
			if err != nil {
				if errors.Is(err, ErrBudgetExceeded) {
					mu.Lock()
					results[i] = fixResult{listIndex: item.listIndex, title: item.title, page: item.physicalIndex, valid: false}
					mu.Unlock()
					return nil
				}
				return err
			}
			if !ok || fixedPage <= 0 || fixedPage > len(pages) {
				mu.Lock()
				results[i] = fixResult{listIndex: item.listIndex, title: item.title, page: item.physicalIndex, valid: false}
				mu.Unlock()
				return nil
			}
			yes, err := idx.checkTitleAppearance(gctx, item.title, pages[fixedPage-1], budget, stats)
			if err != nil {
				if errors.Is(err, ErrBudgetExceeded) {
					mu.Lock()
					results[i] = fixResult{listIndex: item.listIndex, title: item.title, page: fixedPage, valid: false}
					mu.Unlock()
					return nil
				}
				return err
			}
			mu.Lock()
			results[i] = fixResult{listIndex: item.listIndex, title: item.title, page: fixedPage, valid: yes}
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		// Surface as an unresolved batch — caller sees the same set returned.
		if idx.log != nil {
			idx.log.Warn("pageindex.fix.errgroup", glog.Error(err))
		}
		return incorrect
	}
	invalid := make([]incorrectItem, 0, len(results))
	for _, r := range results {
		if !r.valid {
			invalid = append(invalid, incorrectItem{listIndex: r.listIndex, title: r.title, physicalIndex: r.page})
			continue
		}
		if r.listIndex < 0 || r.listIndex >= len(flat) {
			invalid = append(invalid, incorrectItem{listIndex: r.listIndex, title: r.title, physicalIndex: r.page})
			continue
		}
		flat[r.listIndex].node.StartIndex = r.page
	}
	return invalid
}

// neighborPages returns the previous and next correct flat indices' pages,
// defaulting to the document's bounds. Mirrors the prev_correct/next_correct
// search in fix_incorrect_toc.process_and_check_item.
func neighborPages(flat []flatNode, listIndex int, incorrectSet map[int]struct{}, totalPages int) (int, int) {
	prev := 1
	for i := listIndex - 1; i >= 0; i-- {
		if _, bad := incorrectSet[i]; bad {
			continue
		}
		if flat[i].node.StartIndex > 0 {
			prev = flat[i].node.StartIndex
			break
		}
	}
	next := totalPages
	for i := listIndex + 1; i < len(flat); i++ {
		if _, bad := incorrectSet[i]; bad {
			continue
		}
		if flat[i].node.StartIndex > 0 {
			next = flat[i].node.StartIndex
			break
		}
	}
	if prev < 1 {
		prev = 1
	}
	if next < prev {
		next = prev
	}
	if next > totalPages {
		next = totalPages
	}
	return prev, next
}

// buildPhysicalIndexBlock formats pages[prev-1:next] in the upstream
// <physical_index_X> ... <physical_index_X> wrapper.
func buildPhysicalIndexBlock(pages []string, prev, next int) string {
	if prev < 1 {
		prev = 1
	}
	if next > len(pages) {
		next = len(pages)
	}
	if next < prev {
		return ""
	}
	var sb strings.Builder
	for p := prev; p <= next; p++ {
		fmt.Fprintf(&sb, "<physical_index_%d>\n%s\n<physical_index_%d>\n\n", p, pages[p-1], p)
	}
	return sb.String()
}

// singleTOCItemIndexFixer asks the LLM for a corrected physical_index. Returns
// (page, ok, err) where ok=false signals an unparseable response.
func (idx *Indexer) singleTOCItemIndexFixer(ctx context.Context, title, content string, budget *Budget, stats *Stats) (int, bool, error) {
	prompt, err := RenderPrompt(PromptSingleTOCItemIndexFixer, SingleTOCItemIndexFixerVars{SectionTitle: title, Content: content})
	if err != nil {
		return 0, false, err
	}
	req := Request{
		Input:      userInput(prompt),
		Schema:     fixerSchema,
		SchemaName: "toc_item_index_fix",
	}
	resp, err := idx.callLLM(ctx, req, budget, stats)
	if err != nil {
		return 0, false, err
	}
	body := stripCodeFence(resp.Text)
	if body == "" {
		return 0, false, nil
	}
	var out titleFixerResult
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return 0, false, nil
	}
	page, ok := physicalIndexInt(out.PhysicalIndex)
	if !ok || page <= 0 {
		return 0, false, nil
	}
	return page, true, nil
}
