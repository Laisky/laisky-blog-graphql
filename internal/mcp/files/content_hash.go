package files

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashFileContent returns the lowercase hex SHA-256 of the complete stored bytes.
// It is the immutable content-generation identity that binds a file's chunks and
// its summary together (docs/proposals/file_search_file_summaries.md §4.2).
func HashFileContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
