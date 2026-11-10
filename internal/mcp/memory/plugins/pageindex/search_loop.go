package pageindex

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// SearchInput captures the per-call retrieval parameters.
type SearchInput struct {
	Project        string
	Query          string
	PathPrefix     string
	Limit          int
	ValidateSource func(context.Context, string, string) (bool, error)
	// Tenant names the caller whose rolling LLM budget the search draws from.
	Tenant string
	// OnCandidate, when set, is called with every candidate whose source is
	// current, before it is searched; the plugin uses it to refresh degraded trees.
	OnCandidate func(userPath string, entry IndexEntry)
}

// SearchEngine is the smaller interface the plugin uses against the indexer.
type SearchEngine interface {
	GetPageContent(tree *Tree, ranges []PageRange) ([]Chunk, error)
}

// Searcher carries the dependencies the plugin's Search method needs.
type Searcher struct {
	llm    LLM
	store  *SysStore
	cfg    Settings
	engine SearchEngine
	gate   llmGate
}

// NewSearcher constructs the retrieval helper. When engine is the plugin's
// Indexer, search shares its tokenizer and rolling LLM budget, so indexing and
// search draw from one tenant and global budget.
func NewSearcher(llm LLM, store *SysStore, engine SearchEngine, cfg Settings) *Searcher {
	cfg.Limits = cfg.Limits.WithDefaults()
	gate := llmGate{spend: NewSpendGovernor(cfg.Limits)}
	if idx, ok := engine.(*Indexer); ok && idx != nil {
		gate.tok = idx.tok
		if idx.gate.spend != nil {
			gate.spend = idx.gate.spend
		}
	}
	return &Searcher{llm: llm, store: store, cfg: cfg, engine: engine, gate: gate}
}

// Run is the §2.6.2 tree-reasoning loop. It returns a files.SearchResult that
// the plugin layer maps into the wider MCP shape.
func (s *Searcher) Run(ctx context.Context, in SearchInput) (files.SearchResult, error) {
	if s.store == nil {
		return files.SearchResult{}, errors.New("sysstore is nil")
	}
	ix, err := s.store.GetIndex(ctx, in.Project)
	if err != nil {
		return files.SearchResult{}, err
	}
	candidates := filterCandidatesLimited(ix, in.PathPrefix, s.cfg.TreeQuery.CandidateDocs)
	stepBudget := s.cfg.TreeQuery.MaxSteps
	if stepBudget <= 0 {
		stepBudget = 8
	}
	budget := newOperationBudget(int64(s.cfg.TreeQuery.MaxTokens), stepBudget, unlimited, in.Tenant)
	allChunks := make([]files.ChunkEntry, 0, 8)
	for _, cand := range candidates {
		if stepBudget <= 0 || budget.Remaining() <= 0 {
			break
		}
		if in.ValidateSource != nil {
			valid, validateErr := in.ValidateSource(ctx, cand.userPath, cand.entry.SourceContentHash)
			if validateErr != nil || !valid {
				continue
			}
		}
		stepBudget--
		if in.OnCandidate != nil {
			in.OnCandidate(cand.userPath, cand.entry)
		}
		tree, err := s.store.GetTree(ctx, in.Project, cand.entry.DocID)
		if err != nil {
			continue
		}
		ranges, err := s.pickRanges(ctx, tree, in.Query, budget)
		if err != nil {
			// Fallback per P05: return the first node's pages so callers see something.
			ranges = []PageRange{firstNodeRange(tree)}
		}
		chunks, err := s.engine.GetPageContent(tree, ranges)
		if err != nil {
			continue
		}
		// Tree.DocDescription is the authoritative public summary for this document.
		// It is bounded to the shared limits and repeated on every returned chunk so
		// each hit is self-contained (§3.2, §4.5).
		docSummary := publicDocSummary(tree)
		for i, ch := range chunks {
			score := positionDecay(i, len(chunks))
			allChunks = append(allChunks, files.ChunkEntry{
				FilePath:           cand.userPath,
				FileSeekStartBytes: int64(ch.Page) * 1024,
				FileSeekEndBytes:   int64(ch.Page)*1024 + int64(len(ch.Content)),
				ChunkContent:       ch.Content,
				FileSummary:        docSummary,
				Score:              score,
			})
		}
	}
	sort.SliceStable(allChunks, func(i, j int) bool { return allChunks[i].Score > allChunks[j].Score })
	if in.Limit > 0 && len(allChunks) > in.Limit {
		allChunks = allChunks[:in.Limit]
	}
	return files.SearchResult{Chunks: allChunks}, nil
}

func positionDecay(i, n int) float64 {
	if n <= 0 {
		return 0
	}
	return 1.0 / float64(i+1)
}

type rankedCandidate struct {
	userPath string
	entry    IndexEntry
}

func filterCandidates(ix Index, prefix string) []rankedCandidate {
	out := make([]rankedCandidate, 0, len(ix))
	for p, entry := range ix {
		if prefix == "" || prefix == "*" || strings.HasPrefix(p, prefix) {
			out = append(out, rankedCandidate{userPath: p, entry: entry})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].userPath < out[j].userPath })
	return out
}

func firstNodeRange(tree *Tree) PageRange {
	if tree == nil || len(tree.Structure) == 0 {
		return PageRange{Start: 1, End: 1}
	}
	n := tree.Structure[0]
	end := n.EndIndex
	if end < n.StartIndex {
		end = n.StartIndex
	}
	if end <= 0 {
		end = 1
	}
	return PageRange{Start: max(n.StartIndex, 1), End: end}
}

// pickRanges runs the PromptPickPageRanges prompt and parses the JSON output.
// The call is admitted by the shared llmGate, so it counts against the search's
// tree_query budget and the caller's rolling tenant budget.
func (s *Searcher) pickRanges(ctx context.Context, tree *Tree, query string, budget *Budget) ([]PageRange, error) {
	if s.gate.wouldRefuse(budget) {
		return nil, ErrBudgetExceeded
	}
	prompt, err := s.plannerPrompt(tree, query, budget.Remaining())
	if err != nil {
		return nil, err
	}
	req := Request{Input: userInput(prompt)}
	admission, err := s.gate.admit(req, budget)
	if err != nil {
		return nil, err
	}
	resp, err := s.llm.Respond(ctx, admission.bind(req))
	admission.settle(resp)
	if err != nil {
		return nil, err
	}
	return parsePickRangesResponse(resp.Text)
}

// plannerPrompt renders the planner prompt with the most detailed outline whose
// estimated size fits the remaining search budget: the full outline, then the
// outline without summaries, then shallower outlines, then fewer top-level
// sections. It returns ErrBudgetExceeded when not even one section fits, and the
// caller falls back to the first section's pages.
func (s *Searcher) plannerPrompt(tree *Tree, query string, remaining int64) (string, error) {
	var prompt string
	var renderErr error
	forEachOutlineCandidate(tree.Structure, func(outline []*Node) bool {
		body, err := json.Marshal(outline)
		if err != nil {
			renderErr = errors.Wrap(err, "encode index structure")
			return false
		}
		candidate, err := RenderPrompt(PromptPickPageRanges, PickPageRangesVars{
			Query:     query,
			Tree:      string(body),
			MaxRanges: 5,
		})
		if err != nil {
			renderErr = err
			return false
		}
		if int64(len(candidate)) <= remaining || estimateInputTokens(s.gate.tok, Request{Input: userInput(candidate)}) <= remaining {
			prompt = candidate
			return false
		}
		return true
	})
	if renderErr != nil {
		return "", renderErr
	}
	if prompt == "" {
		return "", ErrBudgetExceeded
	}
	return prompt, nil
}

// forEachOutlineCandidate calls fn with progressively smaller outlines of nodes,
// the most detailed first, until fn returns false. An ordinary document stops at
// the first, full outline.
func forEachOutlineCandidate(nodes []*Node, fn func([]*Node) bool) {
	if !fn(CloneOutline(nodes)) {
		return
	}
	bare := stripSummaries(CloneOutline(nodes))
	if !fn(bare) {
		return
	}
	for depth := 3; depth >= 1; depth-- {
		if !fn(limitDepth(bare, depth)) {
			return
		}
	}
	top := limitDepth(bare, 1)
	for n := len(top) / 2; n >= 1; n /= 2 {
		if !fn(top[:n]) {
			return
		}
	}
}

// stripSummaries clears every Summary in an outline copy and returns it.
func stripSummaries(nodes []*Node) []*Node {
	WalkNodes(nodes, func(n *Node) { n.Summary = "" })
	return nodes
}

// limitDepth returns a copy of an outline truncated to depth levels.
func limitDepth(nodes []*Node, depth int) []*Node {
	if len(nodes) == 0 || depth <= 0 {
		return nil
	}
	out := make([]*Node, 0, len(nodes))
	for _, n := range nodes {
		cp := *n
		cp.Children = limitDepth(n.Children, depth-1)
		out = append(out, &cp)
	}
	return out
}

func parsePickRangesResponse(raw string) ([]PageRange, error) {
	body := stripCodeFence(raw)
	if body == "" {
		return nil, errors.New("empty response")
	}
	var wrapper struct {
		Ranges []struct {
			Start  int    `json:"start"`
			End    int    `json:"end"`
			Reason string `json:"reason"`
		} `json:"ranges"`
	}
	if err := json.Unmarshal([]byte(body), &wrapper); err != nil {
		return nil, errors.Wrap(err, "decode ranges")
	}
	out := make([]PageRange, 0, len(wrapper.Ranges))
	for _, r := range wrapper.Ranges {
		if r.End == 0 {
			r.End = r.Start
		}
		if r.Start <= 0 {
			continue
		}
		out = append(out, PageRange{Start: r.Start, End: r.End})
	}
	return out, nil
}
