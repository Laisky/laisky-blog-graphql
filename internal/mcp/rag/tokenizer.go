package rag

import (
	"regexp"
	"strings"
)

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

// tokenize splits the text into lowercase alphanumeric tokens suitable for approximate BM25 scoring.
func tokenize(text string) []string {
	lowered := strings.ToLower(text)
	cleaned := nonWord.Split(lowered, -1)
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
