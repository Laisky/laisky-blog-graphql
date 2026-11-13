package pageindex

import (
	"strings"
	"sync"
	"unicode/utf8"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	glog "github.com/Laisky/zap"
	tk "github.com/tiktoken-go/tokenizer"
)

// Tokenizer counts and encodes tokens for the configured chat model.
type Tokenizer interface {
	Count(text string) int
	Encode(text string) []int
}

// NewTokenizer resolves the closest tiktoken codec for model. Falls back to
// cl100k_base for unknown chat-model names; logs a single WARN once per process.
func NewTokenizer(model string) (Tokenizer, error) {
	enc := encodingForModel(model)
	codec, err := tk.Get(enc)
	if err != nil {
		return nil, errors.Wrapf(err, "load tiktoken codec %q", enc)
	}
	return &tiktokenizer{codec: codec, model: model, encoding: enc}, nil
}

type tiktokenizer struct {
	codec    tk.Codec
	model    string
	encoding tk.Encoding
}

// maxTokenizerRunBytes bounds the longest run of whitespace or non-whitespace
// bytes handed to the codec in one piece. The codec's pre-tokenizer
// (regexp2 with lookahead) and its byte-pair merge are both quadratic in the
// length of one such run, so an untrusted page or prompt made of one long run
// would otherwise cost minutes of CPU. Ordinary text never has runs this long,
// so its count is exactly the codec's count; a cut inside a longer run can only
// add a token at the cut, which keeps the count a safe upper estimate.
const maxTokenizerRunBytes = 256

// Count returns the number of tokens in text. Its cost is linear in len(text):
// runs longer than maxTokenizerRunBytes are counted in bounded segments.
func (t *tiktokenizer) Count(text string) int {
	if text == "" {
		return 0
	}
	total := 0
	forEachBoundedSegment(text, maxTokenizerRunBytes, func(segment string) {
		n, err := t.codec.Count(segment)
		if err != nil {
			n = len(segment)/4 + 1
		}
		total += n
	})
	return total
}

// forEachBoundedSegment calls fn with consecutive segments of text whose runs of
// whitespace or non-whitespace bytes are at most maxRun bytes long. Text without
// such a run is passed whole, and every cut falls on a UTF-8 rune boundary.
func forEachBoundedSegment(text string, maxRun int, fn func(string)) {
	start, runStart := 0, 0
	for i := 1; i < len(text); i++ {
		if isTokenizerSpace(text[i]) != isTokenizerSpace(text[i-1]) {
			runStart = i
			continue
		}
		if i-runStart < maxRun || !utf8.RuneStart(text[i]) {
			continue
		}
		fn(text[start:i])
		start, runStart = i, i
	}
	fn(text[start:])
}

// isTokenizerSpace reports whether b is ASCII whitespace for run splitting.
func isTokenizerSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

func (t *tiktokenizer) Encode(text string) []int {
	if text == "" {
		return nil
	}
	ids, _, err := t.codec.Encode(text)
	if err != nil {
		return nil
	}
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		out = append(out, int(id))
	}
	return out
}

var (
	fallbackOnce sync.Once
	fallbackLog  logSDK.Logger
)

// SetTokenizerFallbackLogger lets tests capture the codec-fallback WARN.
func SetTokenizerFallbackLogger(l logSDK.Logger) {
	fallbackLog = l
	fallbackOnce = sync.Once{}
}

func encodingForModel(model string) tk.Encoding {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case m == "":
		return tk.O200kBase
	case strings.HasPrefix(m, "gpt-5"), strings.HasPrefix(m, "gpt-4o"), strings.HasPrefix(m, "gpt-4.1"),
		strings.HasPrefix(m, "o1-"), strings.HasPrefix(m, "o3-"), strings.HasPrefix(m, "o4-"):
		return tk.O200kBase
	case strings.HasPrefix(m, "gpt-4"), strings.HasPrefix(m, "gpt-3.5-turbo"):
		return tk.Cl100kBase
	}
	fallbackOnce.Do(func() {
		if fallbackLog != nil {
			fallbackLog.Warn("pageindex.tokenizer: falling back to cl100k_base for unknown model", glog.String("model", model))
		}
	})
	return tk.Cl100kBase
}
