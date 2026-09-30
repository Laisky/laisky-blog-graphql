package files

// Shared wire-format and storage identifiers keep these contracts consistent.
const (
	chunkProjectPredicate = " AND c.project = ?"
	indexOperationDelete  = "DELETE"
	requiredTextColumn    = "TEXT NOT NULL DEFAULT ''"
	indexOperationUpsert  = "UPSERT"
	tableMcpFileChunks    = "mcp_file_chunks"
	tableMcpFileIndexJobs = "mcp_file_index_jobs"
	tableMcpFiles         = "mcp_files"
	indexStatusPending    = "pending"
	encodingUTF8          = "utf-8"
)
