package files

// buildPathPrefix returns the SQL LIKE prefix for descendants.
func buildPathPrefix(path string) string {
	if path == "" {
		return "%"
	}
	return path + "/%"
}
