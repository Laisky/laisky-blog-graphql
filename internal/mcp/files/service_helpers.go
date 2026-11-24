package files

import "strings"

// sqlLikeEscape names the escape character used by every LIKE pattern this
// package builds. SQLite has no default LIKE escape, so it is always explicit.
const sqlLikeEscape = ` ESCAPE '\'`

// likePatternEscaper makes LIKE metacharacters literal. "_" is a legal path
// character, so an unescaped prefix would also match unrelated sibling paths.
var likePatternEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// escapeLikePattern returns value with LIKE metacharacters escaped by sqlLikeEscape.
func escapeLikePattern(value string) string {
	return likePatternEscaper.Replace(value)
}

// buildPathPrefix returns the literal SQL LIKE pattern matching every descendant
// of path. Callers must append sqlLikeEscape after the LIKE operand.
func buildPathPrefix(path string) string {
	if path == "" {
		return "%"
	}
	return escapeLikePattern(path) + "/%"
}

// buildLikePrefix returns the literal SQL LIKE pattern matching every string
// that starts with prefix. Callers must append sqlLikeEscape after the operand.
func buildLikePrefix(prefix string) string {
	return escapeLikePattern(prefix) + "%"
}
