package files

import "strings"

// buildPathPrefix returns the SQL LIKE prefix for descendants.
func buildPathPrefix(path string) string {
	if path == "" {
		return "%"
	}
	return path + "/%"
}

// trimLeadingSlash removes a leading slash for relative path handling.
func trimLeadingSlash(path string) string {
	return strings.TrimPrefix(path, "/")
}
