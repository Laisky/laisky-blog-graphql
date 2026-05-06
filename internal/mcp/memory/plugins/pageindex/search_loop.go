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
	Project    string
	Query      string
	PathPrefix string
	Limit      int
}

// SearchEngine is the smaller interface the plugin uses against the indexer.
type SearchEngine interface {
	GetPageContent(tree *Tree, ranges []PageRange) ([]Chunk, error)
}

// Searcher carries the dependencies the plugin's Search method needs.
type Searcher struct {
	llm   LLM
	store *SysStore
	cfg   Settings
	engine SearchEngine
}

// NewSearcher constructs the retrieval helper.
func NewSearcher(llm LLM, store *SysStore, engine SearchEngine, cfg Settings) *Searcher {
	return &Searcher{llm: llm, store: store, cfg: cfg, engine: engine}
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
	candidates := filterCandidates(ix, in.PathPrefix)
	if len(candidates) > s.cfg.TreeQuery.CandidateDocs && s.cfg.TreeQuery.CandidateDocs > 0 {
		candidates = candidates[:s.cfg.TreeQuery.CandidateDocs]
	}
	budget := NewBudget(int64(s.cfg.TreeQuery.MaxTokens))
	stepBudget := s.cfg.TreeQuery.MaxSteps
	if stepBudget <= 0 {
		stepBudget = 8
	}
	allChunks := make([]files.ChunkEntry, 0, 8)
	for _, cand := range candidates {
		if stepBudget <= 0 || budget.Remaining() <= 0 {
			break
		}
		stepBudget--
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
		for i, ch := range chunks {
			score := positionDecay(i, len(chunks))
			allChunks = append(allChunks, files.ChunkEntry{
				FilePath:           cand.userPath,
				FileSeekStartBytes: int64(ch.Page) * 1024,
				FileSeekEndBytes:   int64(ch.Page)*1024 + int64(len(ch.Content)),
				ChunkContent:       ch.Content,
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
func (s *Searcher) pickRanges(ctx context.Context, tree *Tree, query string, budget *Budget) ([]PageRange, error) {
	if budget != nil && budget.Remaining() <= 0 {
		return nil, ErrBudgetExceeded
	}
	outline, _ := json.Marshal(CloneOutline(tree.Structure))
	prompt, err := RenderPrompt(PromptPickPageRanges, PickPageRangesVars{
		Query:     query,
		Tree:      string(outline),
		MaxRanges: 5,
	})
	if err != nil {
		return nil, err
	}
	req := Request{Input: userInput(prompt)}
	resp, err := s.llm.Respond(ctx, req)
	if err != nil {
		return nil, err
	}
	if budget != nil {
		budget.Take(int64(resp.Usage.TotalTokens))
	}
	return parsePickRangesResponse(resp.Text)
}

func parsePickRangesResponse(raw string) ([]PageRange, error) {
	body := stripCodeFence(raw)
	if body == "" {
		return nil, errors.New("empty response")
	}
	var wrapper struct {
		Ranges []struct {
			Start int    `json:"start"`
			End   int    `json:"end"`
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
