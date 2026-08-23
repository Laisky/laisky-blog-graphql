package rag

import "strings"

// Tokenize splits the text into lowercase alphanumeric tokens suitable for approximate BM25 scoring.
func Tokenize(text string) []string {
	lowered := strings.ToLower(text)
	tokens := make([]string, 0)
	seen := make(map[string]struct{})
	tokenStart := -1

	// Scan lowered bytes directly to preserve the ASCII [a-z0-9] contract while
	// avoiding regexp.Split's intermediate slice of every token occurrence.
	for i := 0; i <= len(lowered); i++ {
		if i < len(lowered) && isTokenByte(lowered[i]) {
			if tokenStart == -1 {
				tokenStart = i
			}
			continue
		}
		if tokenStart == -1 {
			continue
		}

		token := lowered[tokenStart:i]
		if _, ok := seen[token]; !ok {
			seen[token] = struct{}{}
			tokens = append(tokens, token)
		}
		tokenStart = -1
	}
	return tokens
}

func isTokenByte(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9')
}
