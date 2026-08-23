package rag

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var referenceNonWord = regexp.MustCompile(`[^a-z0-9]+`)

// benchmarkTokenSink retains benchmark results so the compiler cannot eliminate tokenization as dead code.
var benchmarkTokenSink []string

// TestTokenize verifies the lowercase, ASCII token, order, and deduplication contract.
func TestTokenize(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{name: "empty", text: "", want: []string{}},
		{name: "deduplicate and preserve order", text: "Hello, HELLO world!! world", want: []string{"hello", "world"}},
		{name: "numbers and boundaries", text: "v2/api V2 API-42", want: []string{"v2", "api", "42"}},
		{name: "unicode delimiters", text: "Crème 東京 CAFÉ", want: []string{"cr", "me", "caf"}},
		{name: "unicode lowercase maps to ASCII", text: "AKB AİB", want: []string{"akb", "aib"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens := Tokenize(tt.text)
			require.NotNil(t, tokens)
			require.Equal(t, tt.want, tokens)
		})
	}
}

// TestTokenizeMatchesPreviousImplementation verifies exact equivalence with the previous regexp implementation for arbitrary byte strings.
func TestTokenizeMatchesPreviousImplementation(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 2_000; i++ {
		input := make([]byte, rng.Intn(1_025))
		n, err := rng.Read(input)
		require.NoError(t, err)
		require.Equal(t, len(input), n)

		text := string(input)
		require.Equal(t, tokenizeReference(text), Tokenize(text), "tokenization drift for case %d", i)
	}
}

// BenchmarkTokenizeRegexpSplit measures the previous regexp-based tokenizer.
func BenchmarkTokenizeRegexpSplit(b *testing.B) {
	benchmarkTokenize(b, tokenizeReference)
}

// BenchmarkTokenizeDirectScan measures the direct byte-scanning tokenizer.
func BenchmarkTokenizeDirectScan(b *testing.B) {
	benchmarkTokenize(b, Tokenize)
}

func benchmarkTokenize(b *testing.B, tokenizer func(string) []string) {
	benchmarks := []struct {
		name string
		text string
	}{
		{name: "search_query_128B", text: strings.Repeat("GraphQL memory search token 42. ", 4)},
		{name: "rag_chunk_512B", text: repeatToLength("GraphQL memory indexing keeps stable lexical tokens for documents. ", 512)},
	}

	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(benchmark.text)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchmarkTokenSink = tokenizer(benchmark.text)
			}
			b.StopTimer()
			require.NotEmpty(b, benchmarkTokenSink)
		})
	}
}

func repeatToLength(pattern string, length int) string {
	return strings.Repeat(pattern, (length+len(pattern)-1)/len(pattern))[:length]
}

func tokenizeReference(text string) []string {
	lowered := strings.ToLower(text)
	cleaned := referenceNonWord.Split(lowered, -1)
	tokens := make([]string, 0, len(cleaned))
	seen := make(map[string]struct{})
	for _, token := range cleaned {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}
	return tokens
}
