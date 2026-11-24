package pageindex

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// SearchInput captures the per-call retrieval parameters.
type SearchInput struct {
	Project string
	// FallbackProject, when set, is the storage project of the DefaultProject
	// layer. Its catalog entries join the candidates for paths Project lacks.
	FallbackProject string
	Query           string
	PathPrefix      string
	Limit           int
	ValidateSource  func(context.Context, string, string) (bool, error)
	// Tenant names the caller whose rolling LLM budget the search draws from.
	Tenant string
	// OnCandidate, when set, is called with every candidate whose source is
	// current, before it is searched; the plugin uses it to refresh degraded trees.
	// fromFallback reports that the entry came from FallbackProject.
	OnCandidate func(userPath string, entry IndexEntry, fromFallback bool)
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
	log    logSDK.Logger
	now    func() time.Time

	mu sync.Mutex
	// stats counts planned and fallback candidates; logged and lastLog track the
	// last fallback summary.
	stats   SearchStats
	logged  SearchStats
	lastLog time.Time
}

// NewSearcher constructs the retrieval helper. When engine is the plugin's
// Indexer, search shares its tokenizer and rolling LLM budget, so indexing and
// search draw from one tenant and global budget.
func NewSearcher(llm LLM, store *SysStore, engine SearchEngine, cfg Settings) *Searcher {
	cfg.Limits = cfg.Limits.WithDefaults()
	var tok Tokenizer
	var spend *SpendGovernor
	var log logSDK.Logger
	if idx, ok := engine.(*Indexer); ok && idx != nil {
		tok, spend, log = idx.tok, idx.gate.spend, idx.log
	}
	if spend == nil {
		spend = NewSpendGovernor(cfg.Limits)
	}
	gate := newLLMGate(tok, spend, cfg.Limits)
	return &Searcher{llm: llm, store: store, cfg: cfg, engine: engine, gate: gate, log: log, now: spend.now, lastLog: spend.now()}
}

// Run is the §2.6.2 tree-reasoning loop. It returns a files.SearchResult that
// the plugin layer maps into the wider MCP shape.
func (s *Searcher) Run(ctx context.Context, in SearchInput) (files.SearchResult, error) {
	if s.store == nil {
		return files.SearchResult{}, errors.New("sysstore is nil")
	}
	ix, layers, err := s.candidateIndex(ctx, in)
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
	// guesses are the fallback chunks with their weight, scored once every planned
	// chunk is known so that a guess never outranks a planned section.
	var guesses []weightedChunk
	minPlanned := 1.0
	var terms map[string]struct{}
	for _, cand := range candidates {
		if stepBudget <= 0 || budget.Remaining() <= 0 {
			break
		}
		entry, storageProject, fromFallback, ok := in.selectCandidate(ctx, layers, cand)
		if !ok {
			continue
		}
		cand.entry = entry
		stepBudget--
		if in.OnCandidate != nil {
			in.OnCandidate(cand.userPath, cand.entry, fromFallback)
		}
		tree, err := s.store.GetTree(ctx, storageProject, cand.entry.DocID)
		if err != nil {
			continue
		}
		// Tree.DocDescription is the authoritative public summary for this document.
		// It is bounded to the shared limits and repeated on every returned chunk so
		// each hit is self-contained (§3.2, §4.5).
		docSummary := publicDocSummary(tree)
		ranges, err := s.pickRanges(ctx, tree, in.Query, budget)
		if err != nil {
			// Fallback per P05: without the planner, the sections matching the
			// query's terms (or the first section) are returned, best match first
			// and ranked below every section the planner chose.
			if terms == nil {
				terms = searchTerms(in.Query, maxSearchQueryTerms)
			}
			fallback, matched := fallbackRanges(tree, terms)
			weight := s.recordFallback(cand.entry.DocID, err, matched)
			chunks, err := s.rankedPageContent(tree, fallback)
			if err != nil {
				continue
			}
			for i, ch := range chunks {
				guesses = append(guesses, weightedChunk{entry: chunkEntry(cand.userPath, docSummary, ch, 0), weight: weight * positionDecay(i, len(chunks))})
			}
			continue
		}
		s.recordPlanned()
		chunks, err := s.engine.GetPageContent(tree, ranges)
		if err != nil {
			continue
		}
		for i, ch := range chunks {
			score := positionDecay(i, len(chunks))
			minPlanned = min(minPlanned, score)
			allChunks = append(allChunks, chunkEntry(cand.userPath, docSummary, ch, score))
		}
	}
	for _, g := range guesses {
		// Every weight is below 1, so every guess scores below the lowest
		// planned chunk.
		g.entry.Score = minPlanned * g.weight
		allChunks = append(allChunks, g.entry)
	}
	sort.SliceStable(allChunks, func(i, j int) bool { return allChunks[i].Score > allChunks[j].Score })
	if in.Limit > 0 && len(allChunks) > in.Limit {
		allChunks = allChunks[:in.Limit]
	}
	return files.SearchResult{Chunks: allChunks}, nil
}

// candidateLayers records which merged catalog entries came from the fallback
// catalog, and the fallback entries that a primary entry of the same path hides.
type candidateLayers struct {
	fromFallback map[string]struct{}
	shadowed     map[string]IndexEntry
}

// validSource reports whether entry still describes the visible file at userPath.
func (in SearchInput) validSource(ctx context.Context, userPath string, entry IndexEntry) bool {
	if in.ValidateSource == nil {
		return true
	}
	valid, err := in.ValidateSource(ctx, userPath, entry.SourceContentHash)
	return err == nil && valid
}

// selectCandidate returns the catalog entry to search for cand, its storage
// project, and whether it came from FallbackProject. A primary entry whose
// source no longer validates (for example, left behind when its file was
// deleted outside PageIndex) yields to the fallback entry it shadows, so a
// stale project catalog never hides a visible default.
func (in SearchInput) selectCandidate(ctx context.Context, layers candidateLayers, cand rankedCandidate) (IndexEntry, string, bool, bool) {
	_, fromFallback := layers.fromFallback[cand.userPath]
	storageProject := in.Project
	if fromFallback {
		storageProject = in.FallbackProject
	}
	if in.validSource(ctx, cand.userPath, cand.entry) {
		return cand.entry, storageProject, fromFallback, true
	}
	if shadowed, ok := layers.shadowed[cand.userPath]; ok && in.validSource(ctx, cand.userPath, shadowed) {
		return shadowed, in.FallbackProject, true, true
	}
	return IndexEntry{}, "", false, false
}

// candidateIndex loads the primary catalog and adds the fallback catalog's
// entries for paths the primary lacks. Fallback entries for paths the primary
// also has are kept aside as shadowed alternatives.
func (s *Searcher) candidateIndex(ctx context.Context, in SearchInput) (Index, candidateLayers, error) {
	primary, err := s.store.GetIndex(ctx, in.Project)
	if err != nil || in.FallbackProject == "" {
		return primary, candidateLayers{}, err
	}
	fallback, err := s.store.GetIndex(ctx, in.FallbackProject)
	if err != nil {
		return nil, candidateLayers{}, err
	}
	if len(fallback) == 0 {
		return primary, candidateLayers{}, nil
	}
	merged := make(Index, len(primary)+len(fallback))
	layers := candidateLayers{fromFallback: make(map[string]struct{}, len(fallback)), shadowed: make(map[string]IndexEntry)}
	for userPath, entry := range fallback {
		if _, shadowed := primary[userPath]; shadowed {
			layers.shadowed[userPath] = entry
			continue
		}
		merged[userPath] = entry
		layers.fromFallback[userPath] = struct{}{}
	}
	for userPath, entry := range primary {
		merged[userPath] = entry
	}
	return merged, layers, nil
}

// weightedChunk is a fallback chunk with the share of the lowest planned score
// it receives.
type weightedChunk struct {
	entry  files.ChunkEntry
	weight float64
}

// chunkEntry builds the search hit of chunk ch of the document at userPath.
func chunkEntry(userPath, docSummary string, ch Chunk, score float64) files.ChunkEntry {
	return files.ChunkEntry{
		FilePath:           userPath,
		FileSeekStartBytes: int64(ch.Page) * 1024,
		FileSeekEndBytes:   int64(ch.Page)*1024 + int64(len(ch.Content)),
		ChunkContent:       ch.Content,
		FileSummary:        docSummary,
		Score:              score,
	}
}

// rankedPageContent resolves ranges in their order, each range's pages in page
// order and every page once, so the best-ranked range's pages come first.
func (s *Searcher) rankedPageContent(tree *Tree, ranges []PageRange) ([]Chunk, error) {
	out := make([]Chunk, 0, 8)
	seen := map[int]bool{}
	for _, r := range ranges {
		chunks, err := s.engine.GetPageContent(tree, []PageRange{r})
		if err != nil {
			return nil, err
		}
		for _, ch := range chunks {
			if !seen[ch.Page] {
				seen[ch.Page] = true
				out = append(out, ch)
			}
		}
	}
	return out, nil
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

// pickRanges runs the PromptPickPageRanges prompt and parses the JSON output.
// The call goes through the shared llmGate, so its input and output count against
// the search's tree_query budget and the caller's rolling tenant budget.
func (s *Searcher) pickRanges(ctx context.Context, tree *Tree, query string, budget *Budget) ([]PageRange, error) {
	if err := s.gate.precheck(budget); err != nil {
		return nil, err
	}
	remaining := budget.Remaining()
	prompt, err := s.plannerPrompt(tree, query, remaining-s.gate.answerReserve(remaining))
	if err != nil {
		return nil, err
	}
	resp, err := s.gate.call(ctx, s.llm, Request{Input: userInput(prompt)}, budget)
	if err != nil {
		return nil, err
	}
	return parsePickRangesResponse(resp.Text)
}

// plannerPrompt renders the planner prompt with the most detailed outline whose
// estimated size fits remaining, the input budget left after the planner's answer
// is reserved: the full outline, then the outline without summaries, then
// shallower outlines, then fewer top-level sections. It returns
// ErrBudgetExceeded when not even one section fits, and the caller falls back to
// the sections matching the query (fallbackRanges).
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
