package pageindex

import (
	"strings"
	"sync"

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

func (t *tiktokenizer) Count(text string) int {
	if text == "" {
		return 0
	}
	n, err := t.codec.Count(text)
	if err != nil {
		return len(text) / 4
	}
	return n
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
