# MCP Tool for RAG

Based on the existing MCP tools, a new MCP tool named "extract_key_info" needs to be added. The definition of this tool is as follows:

```
func extract_key_info(query string, materials string, topK int) (contexts []string)
```

- `query`: The user's question or query.
- `materials`: The text materials from which key information needs to be extracted.
- `topK`: Specifies the number of key information entries to return.
- `contexts`: The list of key information returned.

The purpose of this function is to extract the top `topK` key pieces of information from the provided `materials` that are most relevant to the `query`, and return them as `contexts`.

Implementation details:

1. **Text Preprocessing**: Segment the `materials` to ensure each paragraph is of suitable length for subsequent embedding computation.
2. **Embedding Computation**: Use an embeddings model to convert each paragraph and the `query` into vector representations.
3. **Similarity Calculation**: Calculate the similarity between the `query` vector and each paragraph vector, select the top `topK` paragraphs with the highest similarity, and return them.

Technical details:

1. The embeddings model and OpenAI API base are configured in the configuration file.
2. The OpenAI API key is provided by the user via the authentication header.
3. Approximate search uses a hybrid approach: both semantic search with embeddings and keyword search with BM25 are applied.
4. All data must be saved in a PostgreSQL database. The table structure should be designed as needed. Multi-tenancy and multi-tasking must be considered, i.e., data for different users should be isolated (`user_id`), and data for different tasks of the same user should also be isolated (`task_id`).
5. Appropriate indexes should be designed to improve query performance, such as HNSW index.

References:

1. For Golang, PostgreSQL, and pg_vector, refer to docs/ref/pg_vector.md
2. For the current MCP implementation, refer to docs/manual/mcp.md

Task:

Complete a detailed technical implementation manual, including specific scenario descriptions, interface definitions, database design, algorithm flow, etc., and save it to docs/arch/mcp_rag.md
