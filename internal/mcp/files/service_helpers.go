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

// prefixSQL returns a predicate matching col to every value that starts with
// the literal prefix, and its arguments. The escaped LIKE keeps a prefix scan;
// the substr comparison keeps the match case-sensitive on SQLite, whose LIKE
// folds ASCII case. Paths are ASCII, so the byte length is the character length.
// An empty prefix matches every row.
func prefixSQL(col, prefix string) (string, []any) {
	if prefix == "" {
		return "1 = 1", nil
	}
	return "(" + col + " LIKE ?" + sqlLikeEscape + " AND substr(" + col + ", 1, ?) = ?)",
		[]any{escapeLikePattern(prefix) + "%", len(prefix), prefix}
}

// belowPathSQL returns a predicate matching col to every strict descendant of
// path, segment-aware and case-sensitive on both databases, and its arguments.
// The root path "" matches every file.
func belowPathSQL(col, path string) (string, []any) {
	if path == "" {
		return prefixSQL(col, "")
	}
	return prefixSQL(col, path+"/")
}
