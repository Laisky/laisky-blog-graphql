# Chunking & Embeddings

This guide walks through the two building blocks of the GPT chat knowledge base:

- **Chunking** – turning raw documents or text snippets into small, referenceable sections.
- **Embeddings** – storing those chunks inside a FAISS index so the conversational agent can search them.

The latest release introduces a dedicated chunk-only API. You can now preview how material will be segmented before deciding to embed it. All previously shipped embedding capabilities continue to work without any changes.

This server is running at `https://app.laisky.com/` and is available to all registered users.

## Authentication

Most endpoints in the GPT chat service use the same lightweight authentication scheme:

- `Authorization: Bearer <app-key>`
- `X-Laisky-User-Id: <uid>` (optional, defaults to the hashed app key)
- `X-Laisky-User-Is-Free: true|false` (absence or `false` marks the account as paid)
- `X-Laisky-Openai-Api-Base: https://api.openai.com/` (optional override)

Clients that only need chunk previews can reuse these headers; no additional scopes are required.

## Chunk Preview API

- **Route:** `POST /gptchat/chunks`
- **Purpose:** Split plain text or supported document formats (PDF, Word, PowerPoint, Markdown, HTML, TXT) into chunks and inspect the metadata that would later be embedded.
- **Compatibility:** Existing embedding endpoints (`/gptchat/files`, `/gptchat/ctx/...`) remain unchanged.

### Accepted payloads

| Content type          | Description                                                                                                                                                            |
| --------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `application/json`    | Provide `text` directly as a string. Optional fields: `metadata_name`, `chunk_size`, `chunk_overlap`, `max_chunks`.                                                    |
| `multipart/form-data` | Include a `file` field for binary uploads and/or a `text` field. You may also pass `metadata_name`, `chunk_size`, `chunk_overlap`, `max_chunks` as simple form fields. |
| `text/*`              | Raw text body. Optional `X-Laisky-Metadata-Name` header supplies the source name.                                                                                      |
| other binary types    | Send the file body as-is and supply `X-Laisky-File-Name` (with extension) plus optional `X-Laisky-Metadata-Name`.                                                      |

### Chunking parameters

- `chunk_size` – Maximum tokens per chunk. Defaults to `500`.
- `chunk_overlap` – Token overlap between adjacent chunks. Defaults to `30`. Must be smaller than `chunk_size`.
- `max_chunks` – Cap on total chunks returned. Defaults to `600` for free users and `10000` for paid users. The service automatically clips any larger request to your account tier.
- `metadata_name` – Prefix for the generated `source` metadata. When omitted, the service uses the file name (for uploads) or `inline-text`.

### JSON example (inline text)

```bash
curl -X POST "${HOST}/gptchat/chunks" \
	-H "Authorization: Bearer ${APP_KEY}" \
	-H "X-Laisky-User-Id: demo-user" \
	-H "Content-Type: application/json" \
	-d '{
		"text": "My doc intro...",
		"metadata_name": "project-notes",
		"chunk_size": 800,
		"chunk_overlap": 40
	}'
```

### Multipart example (PDF upload)

```bash
curl -X POST "${HOST}/gptchat/chunks" \
	-H "Authorization: Bearer ${APP_KEY}" \
	-H "X-Laisky-User-Id: demo-user" \
	-F "file=@/path/to/report.pdf" \
	-F "metadata_name=report-2025" \
	-F "chunk_size=600" \
	-F "chunk_overlap=60"
```

### Raw binary example (HTML body)

```bash
curl -X POST "${HOST}/gptchat/chunks" \
	-H "Authorization: Bearer ${APP_KEY}" \
	-H "X-Laisky-File-Name: public.html" \
	-H "X-Laisky-Metadata-Name: public-site" \
	--data-binary @page.html
```

### Response payload

```json
{
  "chunks": [
    {
      "text": "...",
      "metadata": {
        "source": "project-notes?chunk=1"
      }
    }
  ],
  "total": 4,
  "chunk_size": 800,
  "chunk_overlap": 40,
  "max_chunks": 600,
  "source": "project-notes",
  "origin": "text"
}
```

- `chunks` contains the raw text plus metadata that downstream embedding calls reuse.
- `origin` indicates whether the request came from `text` or `file` input.
- `source` is the canonical prefix applied to each chunk`s metadata.
- `max_chunks` reflects the resolved cap after applying account limits.

Errors are returned as `400` responses with a descriptive message when inputs are invalid (e.g., unsupported extension, chunk overlap larger than the size, missing file name for binary uploads).

## Embedding pipeline (unchanged)

Once you are satisfied with the chunk preview, continue with the existing endpoints:

1. **Upload & Index** – `POST /gptchat/files`
   - Upload encrypted datasets for private indices. The worker encrypts the source document, builds embeddings, and stores everything in Minio.
2. **Manage datasets** – `GET /gptchat/files`
   - List uploaded datasets and the subset currently active in the user cache.
3. **Build chatbot indices** – `POST /gptchat/ctx/build`
   - Combine multiple datasets into an interactive chatbot index.
4. **Query embeddings** – `GET /gptchat/ctx/chat` or `GET /gptchat/query/query`
   - Retrieve answers from private or prebuilt indices.

All of these endpoints continue to rely on the same metadata emitted by the chunk preview service, so no client-side adjustments are required.

## Best practices

- Use the preview API before uploading large files to ensure the chunk count fits within your quota.
- Keep `metadata_name` stable across uploads so references remain predictable.
- Free accounts can request a smaller `max_chunks` to trim responses without waiting for the auto-limit.
- After chunking, you can reuse the same payload with `embedding_file` workflows to build new datasets.

## Local testing

- Run `pytest tests/test_chunk_preview.py` to exercise the chunk-only endpoint.
- Existing tests (`tests/test_vector_store.py`, etc.) still validate serialization of embedding stores.
- The test suite mocks storage credentials, so no external services are required.
