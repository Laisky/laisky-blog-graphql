// Package library contains helper functions
package library

import "strings"

func appendQuote(v []byte) []byte {
	r := []byte("\"")
	r = append(r, v...)
	r = append(r, '"')
	return r
}

// StripBearerPrefix removes one or more "Bearer " prefixes from the provided
// authorization header and returns the remaining token. Leading and trailing
// whitespace is trimmed from both the header and the resulting token.
func StripBearerPrefix(header string) string {
	trimmed := strings.TrimSpace(header)
	if trimmed == "" {
		return ""
	}

	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return ""
	}

	for len(fields) > 0 && strings.EqualFold(fields[0], "Bearer") {
		fields = fields[1:]
	}

	if len(fields) == 0 {
		return ""
	}

	if len(fields) == 1 && strings.EqualFold(trimmed, fields[0]) {
		return fields[0]
	}

	return strings.Join(fields, " ")
}

// Truncate truncates string to at most n runes.
//
// It iterates over the string using range, which handles UTF-8 runes
// correctly and identifies byte boundaries without allocating a []rune slice.
// When n is less than or equal to zero, it returns the original string
// unchanged. When truncation occurs, it returns a cloned substring so the
// result does not retain the original string's backing bytes.
func Truncate(s string, n int) string {
	if n <= 0 {
		return s
	}

	var count int
	for i := range s {
		if count == n {
			return strings.Clone(s[:i])
		}
		count++
	}

	return s
}
