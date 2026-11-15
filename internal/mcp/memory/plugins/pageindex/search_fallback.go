package pageindex

import (
	"maps"
	"sort"
	"time"
	"unicode"
	"unicode/utf8"

	errors "github.com/Laisky/errors/v2"
	glog "github.com/Laisky/zap"
)

// Causes of a search fallback, the keys of SearchStats.Fallbacks.
const (
	// searchFallbackBudget: a rolling tenant or global budget refused the planner.
	searchFallbackBudget = "budget_exhausted"
	// searchFallbackSearchLimit: the search's own tree_query ceilings left no room
	// for the planner, or its answer was cut at the output ceiling.
	searchFallbackSearchLimit = "search_limit"
	// searchFallbackPlannerFailed: the provider failed or the answer was unusable.
	searchFallbackPlannerFailed = "planner_failed"
)

// Fallback selection bounds and ranking weights.
const (
	// maxFallbackRanges is how many matching sections one fallback returns.
	maxFallbackRanges = 3
	// maxSearchQueryTerms caps the distinct query terms the fallback matches.
	maxSearchQueryTerms = 32
	// maxFallbackNodeTextBytes caps the title or summary bytes read per section.
	maxFallbackNodeTextBytes = 4 << 10
	// matchedFallbackWeight and firstSectionFallbackWeight scale the scores of
	// sections the planner did not choose, so a guess never outranks a section
	// the planner chose for another candidate.
	matchedFallbackWeight      = 0.5
	firstSectionFallbackWeight = 0.25
	// searchFallbackLogInterval is the shortest interval between two summaries.
	searchFallbackLogInterval = time.Minute
)

// SearchStats counts how search chose the sections it returned.
type SearchStats struct {
	// Planned counts candidates whose sections the planner chose.
	Planned uint64
	// FallbackMatched counts candidates answered by sections matching the query
	// terms because the planner did not run or failed.
	FallbackMatched uint64
	// FallbackFirstSection counts fallbacks that matched no section and returned
	// the first one.
	FallbackFirstSection uint64
	// Fallbacks counts every fallback by cause (budget_exhausted, search_limit,
	// planner_failed).
	Fallbacks map[string]uint64
}

// clone returns a deep copy of s.
func (s SearchStats) clone() SearchStats {
	s.Fallbacks = maps.Clone(s.Fallbacks)
	return s
}

// fallbackCause classifies the error that made the planner unavailable.
func fallbackCause(err error) string {
	switch {
	case errors.Is(err, errRollingBudgetExceeded):
		return searchFallbackBudget
	case errors.Is(err, ErrBudgetExceeded):
		return searchFallbackSearchLimit
	default:
		return searchFallbackPlannerFailed
	}
}

// Stats returns a snapshot of the searcher's counters.
func (s *Searcher) Stats() SearchStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats.clone()
}

// recordPlanned counts a candidate answered by the planner.
func (s *Searcher) recordPlanned() {
	s.mu.Lock()
	s.stats.Planned++
	s.mu.Unlock()
}

// recordFallback counts a fallback for docID caused by err, logs it, and returns
// the score weight of its sections. At most once per searchFallbackLogInterval it
// also logs, at warn level, the fallbacks counted since the previous summary.
func (s *Searcher) recordFallback(docID string, err error, matched bool) float64 {
	cause := fallbackCause(err)
	s.mu.Lock()
	if s.stats.Fallbacks == nil {
		s.stats.Fallbacks = map[string]uint64{}
	}
	s.stats.Fallbacks[cause]++
	weight := firstSectionFallbackWeight
	if matched {
		s.stats.FallbackMatched++
		weight = matchedFallbackWeight
	} else {
		s.stats.FallbackFirstSection++
	}
	var summary SearchStats
	emit := false
	if now := s.now(); s.log != nil && now.Sub(s.lastLog) >= searchFallbackLogInterval {
		summary = SearchStats{
			Planned:              s.stats.Planned - s.logged.Planned,
			FallbackMatched:      s.stats.FallbackMatched - s.logged.FallbackMatched,
			FallbackFirstSection: s.stats.FallbackFirstSection - s.logged.FallbackFirstSection,
		}
		s.logged = s.stats.clone()
		s.lastLog = now
		emit = true
	}
	s.mu.Unlock()

	if s.log == nil {
		return weight
	}
	s.log.Debug("pageindex.search.fallback",
		glog.String("doc_id", docID),
		glog.String("cause", cause),
		glog.Bool("matched", matched),
		glog.Error(err),
	)
	if emit {
		s.log.Warn("pageindex.search.fallbacks",
			glog.Uint64("planned", summary.Planned),
			glog.Uint64("fallback_matched", summary.FallbackMatched),
			glog.Uint64("fallback_first_section", summary.FallbackFirstSection),
		)
	}
	return weight
}

// fallbackRanges selects the pages of a candidate whose planner did not run or
// failed, from its outline alone and without an LLM call: the sections whose
// title or summary shares the most query terms (a title term counts twice), at
// most maxFallbackRanges of them, preferring the narrower section and then
// document order, and skipping a section that overlaps one already chosen. It
// returns the first section, and false, when no section matches.
func fallbackRanges(tree *Tree, terms map[string]struct{}) ([]PageRange, bool) {
	if tree == nil || len(tree.Structure) == 0 {
		return []PageRange{{Start: 1, End: 1}}, false
	}
	type hit struct {
		r            PageRange
		score, order int
	}
	var hits []hit
	if len(terms) > 0 {
		order := 0
		WalkNodes(tree.Structure, func(n *Node) {
			order++
			if score := sectionScore(n, terms); score > 0 {
				hits = append(hits, hit{r: sectionRange(n), score: score, order: order})
			}
		})
	}
	if len(hits) == 0 {
		return []PageRange{sectionRange(tree.Structure[0])}, false
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if wi, wj := hits[i].r.End-hits[i].r.Start, hits[j].r.End-hits[j].r.Start; wi != wj {
			return wi < wj
		}
		return hits[i].order < hits[j].order
	})
	chosen := make([]PageRange, 0, maxFallbackRanges)
	for _, h := range hits {
		overlaps := false
		for _, c := range chosen {
			if h.r.Start <= c.End && c.Start <= h.r.End {
				overlaps = true
				break
			}
		}
		if overlaps {
			continue
		}
		chosen = append(chosen, h.r)
		if len(chosen) == maxFallbackRanges {
			break
		}
	}
	return chosen, true
}

// sectionScore returns twice the query terms in n's title plus the further query
// terms in its summary.
func sectionScore(n *Node, terms map[string]struct{}) int {
	matched := make(map[string]struct{}, 4)
	score := 0
	count := func(text string, weight int) {
		eachSearchTerm(boundedPrefix(text, maxFallbackNodeTextBytes), true, func(term string) bool {
			if _, ok := terms[term]; !ok {
				return true
			}
			if _, seen := matched[term]; !seen {
				matched[term] = struct{}{}
				score += weight
			}
			return len(matched) < len(terms)
		})
	}
	count(n.Title, 2)
	count(n.Summary, 1)
	return score
}

// sectionRange returns the pages of n and of every section nested under it: the
// page range of a PDF section, or the heading lines of a Markdown section, whose
// pages are keyed by line.
func sectionRange(n *Node) PageRange {
	first := func(m *Node) int {
		if m.StartIndex > 0 {
			return m.StartIndex
		}
		return m.LineNum
	}
	start := max(first(n), 1)
	end := start
	WalkNodes([]*Node{n}, func(m *Node) {
		end = max(end, first(m), m.EndIndex)
	})
	return PageRange{Start: start, End: end}
}

// boundedPrefix returns at most limit bytes of s, cut at a rune boundary.
func boundedPrefix(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// searchStopWords are English words too common to tell sections apart.
var searchStopWords = map[string]struct{}{
	"an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {}, "can": {}, "do": {}, "does": {},
	"for": {}, "from": {}, "how": {}, "in": {}, "is": {}, "it": {}, "of": {}, "on": {}, "or": {}, "that": {},
	"the": {}, "this": {}, "to": {}, "was": {}, "what": {}, "when": {}, "where": {}, "which": {}, "who": {},
	"why": {}, "with": {},
}

// searchTerms returns the distinct terms of text, at most limit of them in the
// order they first appear (no cap when limit is not positive).
func searchTerms(text string, limit int) map[string]struct{} {
	terms := map[string]struct{}{}
	eachSearchTerm(text, false, func(term string) bool {
		terms[term] = struct{}{}
		return limit <= 0 || len(terms) < limit
	})
	return terms
}

// isCJK reports whether r belongs to a script written without spaces between words.
func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// eachSearchTerm calls fn with every term of text until fn returns false: each
// case-folded run of letters and digits of at least two characters that is not a
// stop word, and every pair of adjacent characters of a CJK run. A CJK run of one
// character is a term of its own; with unigrams, every CJK character is one too,
// so a one-character query term matches inside longer runs.
func eachSearchTerm(text string, unigrams bool, fn func(string) bool) {
	var word, cjk []rune
	flushWord := func() bool {
		defer func() { word = word[:0] }()
		if len(word) < 2 {
			return true
		}
		w := string(word)
		if _, stop := searchStopWords[w]; stop {
			return true
		}
		return fn(w)
	}
	flushCJK := func() bool {
		defer func() { cjk = cjk[:0] }()
		for i := range cjk {
			if (unigrams || len(cjk) == 1) && !fn(string(cjk[i])) {
				return false
			}
			if i+1 < len(cjk) && !fn(string(cjk[i:i+2])) {
				return false
			}
		}
		return true
	}
	for _, r := range text {
		switch {
		case isCJK(r):
			if !flushWord() {
				return
			}
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if !flushCJK() {
				return
			}
			word = append(word, unicode.ToLower(r))
		default:
			if !flushWord() || !flushCJK() {
				return
			}
		}
	}
	if flushWord() {
		flushCJK()
	}
}
