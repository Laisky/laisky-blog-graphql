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

// SearchCatalog is one physical project whose PageIndex catalog a search reads.
type SearchCatalog struct {
	// Project is the physical user project the catalog's documents belong to.
	Project string
	// Storage is the project's tenant-scoped catalog namespace.
	Storage string
}

// SearchInput captures the per-call retrieval parameters.
type SearchInput struct {
	// Catalogs are searched together. Every catalog entry is an independent
	// candidate document, also when two catalogs index the same path, and the
	// candidate and LLM budgets apply to all candidates combined.
	Catalogs   []SearchCatalog
	Query      string
	PathPrefix string
	Limit      int
	// ValidateSource reports whether the live file stored at userPath of the
	// physical project still has expectedHash.
	ValidateSource func(ctx context.Context, project, userPath, expectedHash string) (bool, error)
	// Tenant names the caller whose rolling LLM budget the search draws from.
	Tenant string
	// OnCandidate, when set, is called with every candidate whose source is
	// current, before it is searched; the plugin uses it to refresh degraded trees.
	OnCandidate func(project, userPath string, entry IndexEntry)
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
	candidates, err := s.searchCandidates(ctx, in)
	if err != nil {
		return files.SearchResult{}, err
	}
	// A search of several physical projects names each hit's project, so a hit
	// is readable from its source even where another project shadows the path.
	labelProjects := len(in.Catalogs) > 1
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
		catalog := in.Catalogs[cand.catalog]
		if in.ValidateSource != nil {
			valid, validateErr := in.ValidateSource(ctx, catalog.Project, cand.userPath, cand.entry.SourceContentHash)
			if validateErr != nil || !valid {
				continue
			}
		}
		stepBudget--
		if in.OnCandidate != nil {
			in.OnCandidate(catalog.Project, cand.userPath, cand.entry)
		}
		tree, current := s.currentCandidateTree(ctx, catalog.Storage, cand.entry)
		if !current {
			continue
		}
		hitProject := ""
		if labelProjects {
			hitProject = catalog.Project
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
				guesses = append(guesses, weightedChunk{entry: chunkEntry(hitProject, cand.userPath, docSummary, ch, 0), weight: weight * positionDecay(i, len(chunks))})
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
			allChunks = append(allChunks, chunkEntry(hitProject, cand.userPath, docSummary, ch, score))
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

// searchCandidates keeps the first CandidateDocs catalog entries matching the
// path prefix, ordered by path and then catalog, across every catalog.
func (s *Searcher) searchCandidates(ctx context.Context, in SearchInput) ([]rankedCandidate, error) {
	limit := s.cfg.TreeQuery.CandidateDocs
	var candidates []rankedCandidate
	for i, catalog := range in.Catalogs {
		ix, err := s.store.GetIndex(ctx, catalog.Storage)
		if err != nil {
			return nil, err
		}
		for _, cand := range filterCandidatesLimited(ix, in.PathPrefix, limit) {
			cand.catalog = i
			candidates = append(candidates, cand)
		}
	}
	if len(in.Catalogs) > 1 {
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].userPath != candidates[j].userPath {
				return candidates[i].userPath < candidates[j].userPath
			}
			return candidates[i].catalog < candidates[j].catalog
		})
		if limit > 0 && len(candidates) > limit {
			candidates = candidates[:limit]
		}
	}
	return candidates, nil
}

// currentCandidateTree only returns a tree for the catalog's content generation.
func (s *Searcher) currentCandidateTree(ctx context.Context, project string, entry IndexEntry) (*Tree, bool) {
	tree, err := s.store.GetTree(ctx, project, entry.DocID)
	if err != nil || tree.SourceContentHash != entry.SourceContentHash {
		return nil, false
	}
	return tree, true
}

// weightedChunk is a fallback chunk with the share of the lowest planned score
// it receives.
type weightedChunk struct {
	entry  files.ChunkEntry
	weight float64
}

// chunkEntry builds the search hit of chunk ch of the document at userPath; a
// non-empty project names the physical project that stores it.
func chunkEntry(project, userPath, docSummary string, ch Chunk, score float64) files.ChunkEntry {
	return files.ChunkEntry{
		Project:            project,
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
	// catalog indexes SearchInput.Catalogs.
	catalog int
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
