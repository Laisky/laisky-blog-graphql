package pageindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"golang.org/x/sync/semaphore"
)

// Stats summarizes one Index call.
type Stats struct {
	mu           sync.Mutex
	LLMCalls     int
	InputTokens  int
	OutputTokens int
	Cached       int
	Wallclock    time.Duration
}

// addLLMCall accumulates one LLM-call's accounting under the stats lock so
// concurrent callers (verify, fix, expand) cannot race.
func (s *Stats) addLLMCall(in, out int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.LLMCalls++
	s.InputTokens += in
	s.OutputTokens += out
	s.mu.Unlock()
}

// addCached increments the cached counter atomically.
func (s *Stats) addCached() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.Cached++
	s.mu.Unlock()
}

// IndexOptions tweak per-call indexing behavior.
type IndexOptions struct {
	DocID            string
	AlgorithmVersion string
}

// Deps gathers the indexer's runtime collaborators.
type Deps struct {
	LLM       LLM
	PDF       PDFParser
	Tokenizer Tokenizer
	Cache     Cache
	Sem       *semaphore.Weighted
	Settings  Settings
	Logger    logSDK.Logger
}

// Indexer orchestrates document → tree extraction.
type Indexer struct {
	llm   LLM
	pdf   PDFParser
	tok   Tokenizer
	cache Cache
	sem   *semaphore.Weighted
	cfg   Settings
	log   logSDK.Logger
}

// NewIndexer wires the indexer fields, supplying defaults where allowed.
func NewIndexer(deps Deps) (*Indexer, error) {
	if deps.LLM == nil {
		return nil, errors.New("llm is nil")
	}
	if deps.Tokenizer == nil {
		return nil, errors.New("tokenizer is nil")
	}
	if deps.PDF == nil {
		p, err := NewPDFParser(deps.Settings.PDF.TextParser, deps.Settings.PDF.OutlineParser)
		if err != nil {
			return nil, errors.Wrap(err, "default pdf parser")
		}
		deps.PDF = p
	}
	if deps.Cache == nil {
		c, err := NewCache(CacheConfig{Enabled: false})
		if err != nil {
			return nil, errors.Wrap(err, "default cache")
		}
		deps.Cache = c
	}
	if deps.Sem == nil {
		w := deps.Settings.Indexer.MaxConcurrency
		if w <= 0 {
			w = 8
		}
		deps.Sem = semaphore.NewWeighted(int64(w))
	}
	return &Indexer{
		llm:   deps.LLM,
		pdf:   deps.PDF,
		tok:   deps.Tokenizer,
		cache: deps.Cache,
		sem:   deps.Sem,
		cfg:   deps.Settings,
		log:   deps.Logger,
	}, nil
}

// Index drives the §2.6.4.1 pipeline against the supplied bytes and returns the tree.
func (idx *Indexer) Index(ctx context.Context, kind DocKind, bytes []byte, opts IndexOptions, progress chan<- Progress) (*Tree, *Stats, error) {
	rep := NewReporter(progress, idx.log)
	stats := &Stats{}
	start := time.Now()
	docID := opts.DocID
	if docID == "" {
		h := sha256.Sum256(bytes)
		docID = hex.EncodeToString(h[:])
	}
	algoVer := opts.AlgorithmVersion
	if algoVer == "" {
		algoVer = AlgorithmVersion
	}
	rep.Report(Progress{Phase: "init", Percent: 0, Message: "begin"})
	var tree *Tree
	var err error
	switch kind {
	case KindPDF:
		tree, err = idx.runPDF(ctx, bytes, rep, stats)
	case KindMarkdown:
		tree, err = idx.runMarkdown(ctx, bytes, rep, stats)
	default:
		return nil, nil, errors.Errorf("unsupported kind %q", kind)
	}
	if err != nil {
		return nil, nil, err
	}
	tree.DocID = docID
	tree.Type = kind
	tree.AlgorithmVer = algoVer
	tree.IndexedAt = time.Now().UTC().Format(time.RFC3339Nano)
	stats.Wallclock = time.Since(start)
	rep.Report(Progress{Phase: "done", Percent: 100, TokensIn: stats.InputTokens, TokensOut: stats.OutputTokens, LLMCalls: stats.LLMCalls})
	return tree, stats, nil
}

// callLLM is the cache-aware, budgeted entry point used by every phase.
func (idx *Indexer) callLLM(ctx context.Context, req Request, budget *Budget, stats *Stats) (*Response, error) {
	if budget != nil && budget.Remaining() <= 0 {
		return nil, ErrBudgetExceeded
	}
	if req.PromptHash == [32]byte{} {
		req.PromptHash = HashRequest(req)
	}
	if cached, ok, err := idx.cache.Get(req.PromptHash); err == nil && ok {
		stats.addCached()
		return cached, nil
	}
	if idx.sem != nil {
		if err := idx.sem.Acquire(ctx, 1); err != nil {
			return nil, err
		}
		defer idx.sem.Release(1)
	}
	resp, err := idx.llm.Respond(ctx, req)
	if err != nil {
		return nil, err
	}
	stats.addLLMCall(resp.Usage.InputTokens, resp.Usage.OutputTokens)
	if budget != nil {
		budget.Take(int64(resp.Usage.TotalTokens))
	}
	if err := idx.cache.Put(req.PromptHash, resp); err != nil && idx.log != nil {
		idx.log.Warn(fmt.Sprintf("pageindex.cache.put: %v", err))
	}
	return resp, nil
}

// GetPageContent resolves page ranges against the cached tree's pages.
func (idx *Indexer) GetPageContent(tree *Tree, ranges []PageRange) ([]Chunk, error) {
	if tree == nil {
		return nil, errors.New("tree is nil")
	}
	if len(tree.Pages) == 0 {
		return nil, nil
	}
	pageMap := make(map[int]Page, len(tree.Pages))
	for _, p := range tree.Pages {
		pageMap[p.Page] = p
	}
	out := make([]Chunk, 0, 8)
	seen := map[int]bool{}
	for _, r := range ranges {
		if r.Start <= 0 {
			r.Start = 1
		}
		if r.End <= 0 || r.End < r.Start {
			r.End = r.Start
		}
		for p := r.Start; p <= r.End; p++ {
			if seen[p] {
				continue
			}
			seen[p] = true
			page, ok := pageMap[p]
			if !ok {
				continue
			}
			out = append(out, Chunk{DocID: tree.DocID, Page: page.Page, LineNum: page.LineNum, Content: page.Content})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Page < out[j].Page })
	return out, nil
}

// GetDocumentStructure returns a token-light outline view.
func (idx *Indexer) GetDocumentStructure(tree *Tree) StructureView {
	if tree == nil {
		return StructureView{}
	}
	return StructureView{
		DocID:          tree.DocID,
		DocName:        tree.DocName,
		DocDescription: tree.DocDescription,
		PageCount:      tree.PageCount,
		LineCount:      tree.LineCount,
		Outline:        CloneOutline(tree.Structure),
	}
}

// jsonInput formats one InputItem helper used across pipelines.
func userInput(content string) []InputItem {
	return []InputItem{{Role: "user", Content: content}}
}

// parseSimpleAnswer reads a free-text "yes"/"no"-style field from JSON-ish text.
func parseSimpleAnswer(raw, key string) string {
	v := extractJSONField(raw, key)
	if v == "" {
		return "no"
	}
	v = strings.ToLower(strings.TrimSpace(v))
	if strings.HasPrefix(v, "yes") {
		return "yes"
	}
	return "no"
}

// extractJSONField pulls a top-level string field from a JSON-ish blob.
// LLMs often wrap responses in ```json fences so we strip those defensively.
func extractJSONField(raw, key string) string {
	body := stripCodeFence(raw)
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err == nil {
		if v, ok := m[key]; ok {
			switch t := v.(type) {
			case string:
				return t
			case bool:
				if t {
					return "yes"
				}
				return "no"
			}
		}
	}
	return ""
}

func stripCodeFence(s string) string {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "```") {
		idx := strings.Index(t, "\n")
		if idx > 0 {
			t = t[idx+1:]
		}
		if strings.HasSuffix(t, "```") {
			t = t[:len(t)-3]
		}
	}
	return strings.TrimSpace(t)
}

// parseTOCList unmarshals a JSON array (possibly wrapped in {table_of_contents:[...]}).
func parseTOCList(raw string) ([]map[string]any, error) {
	body := stripCodeFence(raw)
	if body == "" {
		return nil, nil
	}
	if strings.HasPrefix(body, "{") {
		var wrapper map[string]any
		if err := json.Unmarshal([]byte(body), &wrapper); err == nil {
			if list, ok := wrapper["table_of_contents"].([]any); ok {
				return convertList(list), nil
			}
		}
	}
	var arr []any
	if err := json.Unmarshal([]byte(body), &arr); err != nil {
		return nil, errors.Wrap(err, "decode toc list")
	}
	return convertList(arr), nil
}

func convertList(list []any) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// physicalIndexInt parses values like "<physical_index_42>" or 42 into an int.
func physicalIndexInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case string:
		s := strings.TrimSpace(t)
		s = strings.TrimPrefix(s, "<")
		s = strings.TrimSuffix(s, ">")
		s = strings.TrimPrefix(s, "physical_index_")
		if n, err := strconv.Atoi(s); err == nil {
			return n, true
		}
	}
	return 0, false
}
