---
title: Image + text support for get_user_request
status: shipped
owner: mcp team
created: 2026-04-19
updated: 2026-04-19
---

# Image + Text Support for `get_user_request` — Change Manual

## 1. Background

### 1.1 Current state

`get_user_request` is the MCP tool that lets a user append follow-up instructions to an AI agent while a task is running.
Today's pipeline ([internal/mcp/tools/get_user_request.go:286-302](../../internal/mcp/tools/get_user_request.go#L286-L302)):

- Web UI submits plain text → `POST /api/requests` → DB stores `Request.Content string` → `buildCommandsResponse` returns
  ```json
  { "commands": [{ "content": "..." }] }
  ```
  as a single `TextContent` block.

### 1.2 User requirement

Users want to attach images (screenshots, diagrams, reference designs) to their submissions so the AI agent can see both the prose and the visual context.

### 1.3 Ecosystem status (verified 2026-04)

| Capability                                    | MCP Spec 2025-11-25 | Claude Code                                                                                                                                                             | Codex CLI (v0.117+)                                                                                                                             |
| --------------------------------------------- | ------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `image` content block (inline base64)         | Native support      | Accepts `image/jpeg\|png\|gif\|webp`; base64 counts against `MAX_MCP_OUTPUT_TOKENS` (default 25k)                                                                       | Renders as `<image content>` placeholder; the model cannot see it ([openai/codex#4819](https://github.com/openai/codex/issues/4819) still open) |
| `resource_link` content block (URI reference) | Native support      | Agent can fetch on demand                                                                                                                                               | Usable via the `view_image` tool introduced in v0.115.0 (2026-03-16)                                                                            |
| Mixed text + image                            | Supported           | Known bug [anthropics/claude-code#14150](https://github.com/anthropics/claude-code/issues/14150) where some builds spool the result to disk instead of reading directly | Equivalent to image-only                                                                                                                        |
| `structuredContent`                           | Supported           | Supported (machine-readable)                                                                                                                                            | Supported (machine-readable)                                                                                                                    |

### 1.4 Key conclusions

- **A single `ImageContent` block will not work on Codex today.** We adopt a **dual-channel** response: each image is emitted both as an `ImageContent` block (so Claude Code sees it natively) **and** as a `resource_link` plus a URL field inside `structuredContent` (so Codex can load it with `view_image`). One server-side response serves both clients without sniffing.
- **Image bytes live in MinIO, not Postgres.** Postgres keeps only metadata. This enables built-in presigned URLs, lifecycle-based TTL, and trivially-scalable storage.
- **All stored images are PNG.** Any accepted input format is decoded, normalized (orientation, resize), and re-encoded as PNG so Claude Code / Codex only ever deal with a single, universally supported format.
- **The MinIO endpoint (`https://s3.laisky.com`) is publicly reachable.** Presigned URLs are handed directly to the agent; no proxy endpoint is needed. This is the only retrieval path — there is no per-server streaming fallback.

## 2. Goals and Constraints

### 2.1 Goals

- G1. Users can attach 1..5 images per `get_user_request` submission, either as local file uploads or as external `image_url` references.
- G2. Both Claude Code and Codex CLI agents can effectively "see" the attached images.
- G3. Fully backward compatible for pure-text submissions — existing callers see no change.
- G4. A single tool response stays within the default `MAX_MCP_OUTPUT_TOKENS` budget of 25k.
- G5. Image bytes are protected by short-lived, unguessable URLs; cross-tenant access is impossible.
- G6. Multi-tenant isolation: every object is namespaced by `UserIdentity`; quota and authorization are enforced per user.
- G7. Bounded cost: each user occupies at most 100 MiB of storage at any time; objects auto-expire after 7 days.
- G8. External URLs are fetched server-side with SSRF protection; the same normalization pipeline applies whether bytes came from an upload or a URL.
- G9. Compose UX: an "add image" button plus drag-and-drop on the compose area; thumbnails render below the textarea with a hover-revealed delete button; the button is disabled once 5 images are attached.

### 2.2 Non-goals

- No GIF animation support — accepted as input (single-frame extract) but not preserved.
- No HEIC / AVIF support in v1 (requires cgo libraries; revisit later).
- No audio / video / arbitrary attachments.
- No server-side OCR or AI image pre-processing.
- No permanent archival — storage is transient (7 days).

### 2.3 Hard constraints

- Per-image hard cap: **20 MiB** raw bytes, applied identically to uploaded files and URL-fetched bodies (pre-conversion).
- Server-side normalization: decode → fix EXIF orientation → resize so longest edge ≤ 1536 px → re-encode as PNG. Dimensions are rejected if either side exceeds 8192 px before resize (decode-bomb guard).
- Per-request: max **5 images**, counting files and `image_url`s together.
- Accepted input MIME types: `image/jpeg`, `image/png`, `image/webp`, `image/bmp`, `image/tiff`, `image/gif` (first frame). All other types (notably `image/svg+xml`, `image/heic`) are rejected.
- Stored object MIME: always `image/png`.
- Per-user quota: 100 MiB across live (non-expired) objects, based on **post-normalization PNG size**. Because the normalization always resizes to ≤ 1536 px, a single image's on-disk footprint rarely exceeds ~6 MB regardless of the 20 MiB input cap.
- Object TTL: 7 days from upload (enforced by MinIO bucket lifecycle). Re-uploading the same image from the same user refreshes the TTL.
- Total inline base64 per MCP response ≤ 80 KiB (~20k-token budget, leaving 5k headroom for text). Anything beyond degrades to `resource_link`-only.
- `image_url` constraints (SSRF guard, §3.8): HTTPS only (HTTP blocked by default), no private / loopback / link-local / cloud-metadata IPs after DNS resolution, max 3 redirects, 15 s total fetch deadline, 20 MiB stream cap.
- All timestamps are UTC (AGENTS.md rule).

## 3. Design Overview

### 3.1 Architecture

```
+----------+  multipart/form-data          +-----------------+  PUT (PNG)   +----------+
| Web UI   |------------------------------>|  POST /api/reqs |------------->|  MinIO   |
| (button  |  text + images[] + urls[]     |  handleCreate   | 7d lifecycle |  bucket/ |
|  / drag  |                               |  + URL fetch    |              |  prefix/ |
|  drop)   |                               |  + normalize    |              | user/sha |
+----------+                               |  + quota check  |              +----+-----+
                                           +-------+---------+                   |
                     external image URLs ⇑         | metadata                    |
                     (SSRF-guarded fetcher)        v                             |
                                           +---------------+                     |
                                           |  Postgres     |                     |
                                           |  images+quota |                     |
                                           +-------+-------+                     |
                                                   |                             |
+-------------+  tools/call         +--------------v----+  presign GET           |
| Claude Code |-------------------->| get_user_request  |----------------------->|
| / Codex CLI |<--------------------|  buildResponse    |<-- fetch (small) ------|
+-------------+   mixed content     +-------------------+                        |
   |- TextContent (command JSON with image metadata + presigned URLs)            |
   |- ImageContent (base64 inline, small & within budget)  <- Claude             |
   |- ResourceLink (presigned URL)                         <- Codex view_image --+
   \- structuredContent { commands: [...] }
```

### 3.2 MCP response shape

For each `Request`, append to `CallToolResult.Content` in order:

1. `TextContent` carrying a JSON serialization of `{"command": i, "content": "...", "images":[{"id","mime","url","width","height","sha256","expires_at"}]}` (human-readable summary plus `[img:<id>]` inline placeholders).
2. For each image:
   - If the inline budget allows → `ImageContent{Type:"image", Data:base64, MIMEType:"image/png"}` (Claude reads this).
   - Always also append `ResourceLink{Type:"resource_link", URI: presignedURL, Name:"<sha256>.png", MIMEType:"image/png"}` (Codex loads this).
3. `structuredContent`: `{"commands":[...], "protocol_version":"v2"}` for machine-readable parsing.

**Backward compatibility**: if a request has no images, the output is byte-identical to today's `NewToolResultJSON` path.

### 3.3 MinIO storage layout

- Endpoint: `https://s3.laisky.com` (public, reachable from Claude Code and Codex networks). Bucket + prefix are provided via config.
- Client: wrapped around `github.com/minio/minio-go/v7`, following the pattern in [/home/laisky/repo/laisky/go-ramjet/library/s3/minio.go](/home/laisky/repo/laisky/go-ramjet/library/s3/minio.go) but without `sync.Once` (so that a transient init failure can be retried).
- Object key: `<prefix>/<UserIdentity>/<sha256>.png`.
  - `UserIdentity` comes from the authorization context (`askuser.AuthorizationContext.UserIdentity`), already sanitized.
  - `<sha256>` is the hex digest of the **post-conversion PNG bytes**, so identical normalized images dedup within a user's prefix.
- Public URL shape: `https://s3.laisky.com/<bucket>/<prefix>/<UserIdentity>/<sha256>.png` plus presign query parameters.
- Content type: always `image/png`; content disposition `inline; filename="<sha256>.png"`.
- Metadata (MinIO user-meta headers, `x-amz-meta-*`):
  - `x-amz-meta-api-key-hash`: lets an out-of-band audit job reconcile ownership.
  - `x-amz-meta-original-mime`: the uploaded MIME before conversion (for debugging).
  - `x-amz-meta-uploaded-at`: UTC RFC3339.
- TTL: a bucket-level lifecycle rule restricted to the `<prefix>/` path expires objects 7 days after creation. The rule is created idempotently at server bootstrap via `client.SetBucketLifecycle`; missing permissions only warn (local dev) but fail startup in production.
- Download: MinIO `PresignedGetObject(ctx, bucket, key, 30*time.Minute, nil)` — re-issued per MCP tool call. 30 minutes covers an agent's typical round-trip without extending exposure.

### 3.4 Server-side image pipeline

The pipeline accepts bytes from either a multipart file part or an `image_url` (fetched per §3.8). From step 1 onward the two paths are identical.

1. **Acquire bytes**:
   - Uploaded file: `io.LimitReader(part, 20*MiB+1)` → error 413 if over 20 MiB.
   - `image_url`: §3.7 fetcher returns a `[]byte` already capped at 20 MiB and only after SSRF checks.
2. **Detect**: `http.DetectContentType(head512)` + filename/URL extension; reject if not in the allowed input set.
3. **Decode config**: `image.DecodeConfig` — reject if either dimension > 8192 px (decode-bomb guard). Registered decoders: `image/jpeg`, `image/png`, `image/gif`, `golang.org/x/image/webp`, `.../bmp`, `.../tiff`.
4. **Decode** the full image.
5. **Orient**: if the input is JPEG, parse EXIF and apply rotation/flip.
6. **Resize**: if `max(w, h) > 1536`, scale with `golang.org/x/image/draw.CatmullRom` to fit.
7. **Encode** as PNG (`png.Encoder{CompressionLevel: png.DefaultCompression}`).
8. **Hash**: `sha256.Sum256(pngBytes)`.
9. **Quota check** (§3.5) using `len(pngBytes)`. On failure: delete no temp state, return 413.
10. **Idempotent PUT**: `client.PutObject(ctx, bucket, key, bytes.NewReader(pngBytes), size, opts)`. Overwriting is fine — same SHA means same content.
11. **DB upsert**: record metadata + refresh `expires_at = now() + 7d`; `original_mime` records the pre-normalization MIME; `source_url` is nullable and set when the image came via `image_url`.
12. **Return** the metadata row to the caller.

All steps run inside the existing `handleCreate` transaction. The URL fetch (§3.8) happens before the DB transaction opens so that network I/O doesn't hold database locks. If any PUT or fetch fails, the request is not persisted (all-or-nothing).

### 3.5 Quota accounting

- Table `mcp_user_image_refs`:
  ```
  id             UUID PK
  user_identity  TEXT NOT NULL
  api_key_hash   TEXT NOT NULL
  sha256         TEXT NOT NULL
  size_bytes     INT  NOT NULL
  width          INT
  height         INT
  original_mime  TEXT
  created_at     TIMESTAMPTZ NOT NULL
  expires_at     TIMESTAMPTZ NOT NULL  -- created_at + 7d
  UNIQUE (user_identity, sha256)
  INDEX (user_identity, expires_at)
  ```
- Table `mcp_user_request_image_links` (many-to-many between `Request` and `mcp_user_image_refs`):
  ```
  request_id UUID FK → Request(id)
  image_id   UUID FK → mcp_user_image_refs(id)
  sort_order INT NOT NULL
  PRIMARY KEY (request_id, sort_order)
  ```
- Quota query: `SELECT COALESCE(SUM(size_bytes), 0) FROM mcp_user_image_refs WHERE user_identity = $1 AND expires_at > NOW()` (NOW() uses UTC via `SET TIME ZONE 'UTC'` at connection init).
- Upload flow:
  1. Inside a transaction: `SELECT ... FOR UPDATE` on any existing `(user_identity, sha256)` row.
  2. If the row exists and is not expired → refresh `expires_at`, no quota charge.
  3. Else sum current usage; reject if `sum + new_size > quota`.
  4. Insert or upsert `mcp_user_image_refs`.
- Cleanup: a periodic job (e.g. hourly) deletes rows with `expires_at < NOW()`. Missing the job is not a correctness issue because queries always filter by `expires_at`.

### 3.6 Authorization & URL issuing

There is no custom HMAC and no proxy fallback. Two independent guards protect images:

- **Request creation path**: standard `mcpauth.HTTPMiddleware` enforces that the caller holds a valid API key for `UserIdentity`. Quota and ownership are enforced in SQL (`WHERE user_identity = $1`).
- **Image retrieval**: MinIO `PresignedGetObject` with 30-minute validity on a public endpoint (`https://s3.laisky.com`). The URL is unguessable because the path contains the SHA256 of the content and the query contains an AWS V4 signature. Agents treat it opaquely. URLs are re-issued every time the MCP tool runs, so a leaked URL ages out within 30 minutes.

Because retrieval bypasses our server entirely, the proposal does not add any download endpoint under `/api/requests/:id/images`.

### 3.7 `image_url` fetching and SSRF protection

When a caller provides an `image_url`, the server performs the fetch itself — the browser never streams bytes from the third party, so we can enforce identical guards for all intake paths.

Fetcher rules:

- **Scheme allowlist**: `https` is allowed by default. `http` is disabled by default but can be enabled per-environment via config (not recommended for production).
- **DNS guard**: resolve the host via `net.DefaultResolver.LookupIPAddr`. Every resolved IP must pass `isPublic`:
  - Not loopback (`127.0.0.0/8`, `::1`).
  - Not RFC 1918 (`10/8`, `172.16/12`, `192.168/16`) or IPv6 ULA (`fc00::/7`).
  - Not link-local (`169.254/16`, `fe80::/10`).
  - Not the cloud metadata endpoint (`169.254.169.254`) — already covered by link-local, but enforced as a specific deny.
  - Not multicast or unspecified.
- **Connection-level enforcement**: use an `http.Transport` with a custom `DialContext` that re-checks the resolved IP at connect time, preventing TOCTOU where DNS resolves to a safe address first and a malicious one at connect.
- **Redirects**: follow up to 3 redirects; every hop re-runs the scheme and DNS guards.
- **Timeouts**: 5 s TLS handshake, 10 s response headers, 15 s total (including body).
- **Body cap**: stream through `io.LimitReader(resp.Body, 20*MiB+1)`; if read count exceeds 20 MiB, abort with `ErrImageTooLarge`.
- **Content-Type hint**: server-supplied `Content-Type` is only a hint; the real check is `http.DetectContentType` over the first 512 bytes (§3.4 step 2).
- **Dedup with upload path**: after fetch, bytes go through the same §3.4 pipeline. The pipeline's SHA256 dedup applies uniformly: the same image attached by URL and by upload produces the same object.
- **Source record**: the resulting `mcp_user_image_refs` row stores the fetched URL in `source_url` (nullable TEXT) for auditing. The URL is not used for retrieval; agents still receive presigned MinIO URLs.

Errors surface as new codes: `ErrURLBlocked` (scheme / private IP), `ErrURLFetchFailed` (transport), `ErrURLTimeout`, `ErrImageTooLarge` (body > 20 MiB).

### 3.8 Token budget algorithm

```go
const (
    PerCallBudgetBytes = 80 * 1024 // ~20k tokens of base64
    PerImageInlineMax  = 40 * 1024 // per-image inline ceiling
)

remaining := PerCallBudgetBytes
for _, img := range images {
    if len(img.Base64) <= min(remaining, PerImageInlineMax) {
        emitImageContent(img)
        remaining -= len(img.Base64)
    }
    emitResourceLink(img) // always emit, so Codex always has a URL
}
```

The algorithm is extracted into a standalone function for unit testing. Because stored images are PNG ≤ 1536 px, many real uploads (screenshots, UI mockups) compress well enough to stay within `PerImageInlineMax`; photographs typically don't, which is fine because `ResourceLink` covers them.

## 4. Change List

### 4.1 Data layer

| #   | File                                       | Change                                                                                                                                                                                                                                    |
| --- | ------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D1  | `internal/mcp/userrequests/service.go`     | Add two migrations: `mcp_user_image_refs` and `mcp_user_request_image_links` (schemas in §3.5). No `BYTEA` column; images live in MinIO.                                                                                                  |
| D2  | `internal/mcp/userrequests/models.go`      | Add `RequestImage` struct (metadata only: `ID`, `Key`, `SHA256`, `SizeBytes`, `Width`, `Height`, `MIMEType`, `ExpiresAt`). Extend `Request` with `Images []RequestImage`.                                                                 |
| D3  | `internal/mcp/userrequests/service.go`     | `CreateRequest(ctx, auth, content, taskID, normalizedImages []UploadedImage)`; transaction upserts refs, inserts links. `ConsumeAllPending` / `ConsumeFirstPending` preload images with a single `JOIN` filtered by `expires_at > NOW()`. |
| D4  | `internal/mcp/userrequests/quota.go` (new) | `ReserveQuota(ctx, userIdentity, sha256, newSize, quotaBytes) (existingID *uuid.UUID, err error)`; returns `ErrQuotaExceeded`, `ErrImageAlreadyExists`. Uses `SELECT ... FOR UPDATE` to prevent TOCTOU.                                   |
| D5  | `internal/mcp/userrequests/gc.go` (new)    | `GCExpiredRefs(ctx)`; scheduled via existing cron infra. Deletes expired ref rows (cascades through links). Never deletes MinIO objects — lifecycle handles that.                                                                         |

All DB access uses parameterized queries (AGENTS.md: SQL injection rule). All timestamps are UTC.

### 4.2 Storage layer (new)

| #   | File                                       | Change                                                                                                                                                                                                                                                               |
| --- | ------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| M1  | `internal/mcp/storage/minio.go` (new)      | Wraps `minio-go/v7`. `NewClient(cfg)` initializes with `credentials.NewStaticV4`, HTTPS on. Uses a double-checked `sync.RWMutex` (not `sync.Once`) so that a transient DNS/TCP failure at startup can retry on the next call.                                        |
| M2  | `internal/mcp/storage/minio.go`            | `Put(ctx, key, body, size, contentType, userMeta) error`; `PresignedGet(ctx, key, ttl) (url string, err error)`; `EnsureLifecycle(ctx, bucket, prefix, days)` idempotently installs the 7-day expiry rule at bootstrap.                                              |
| M3  | `internal/mcp/imageproc/pipeline.go` (new) | `Normalize(raw []byte, originalFilename string) (pngBytes []byte, w, h int, sha string, originalMIME string, err error)` implements §3.4 steps 2–8. Decodes via stdlib + `golang.org/x/image/{webp,bmp,tiff}`. Uses `golang.org/x/image/draw.CatmullRom` for resize. |
| M4  | `internal/mcp/imageproc/exif.go` (new)     | JPEG EXIF orientation parsing (no full EXIF lib; only tag 0x0112 is needed). Depends on `github.com/rwcarlsen/goexif/exif` if already vendored; otherwise a ~80-line standalone parser keeps the dependency surface small.                                           |
| M5  | `internal/mcp/imageproc/urlfetch.go` (new) | Implements §3.7 SSRF-guarded fetcher: scheme allowlist, `LookupIPAddr` + custom `DialContext` rechecking resolved IP, redirect cap, body limit, timeouts. Exposes `Fetch(ctx, url string) ([]byte, string /*mime hint*/, error)`.                                    |

### 4.3 HTTP API layer

| #   | File                                                                                                 | Change                                                                                                                                                                                                                                                                      |
| --- | ---------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| H1  | [internal/mcp/userrequests/http.go:185](../../internal/mcp/userrequests/http.go#L185) `handleCreate` | Branch on `Content-Type`: JSON (with optional `image_urls[]`) → existing path extended; `multipart/form-data` → parse `content`, `task_id`, `images[]` file parts and `image_urls[]` text parts. Per attachment: if it is a URL, run §3.7 fetcher first; then feed into §3.4 pipeline; then §3.5 quota check; then MinIO PUT; then DB upsert. Enforce a total count of 5 across files and URLs. Semantic 4xx errors for quota / MIME / size / SSRF. Body limit 110 MiB (5 × 20 MiB + slack). |
| H2  | [internal/mcp/userrequests/http.go](../../internal/mcp/userrequests/http.go) response serializer     | Populate each image's `url` field with a fresh presigned MinIO URL so the UI can render thumbnails without going through our server.                                                                                                                                        |
| H3  | new file `internal/mcp/userrequests/quota_http.go`                                                   | `GET /api/quota` returns `{used_bytes, quota_bytes, object_count, ttl_days}` for the composer UI (see §5.2).                                                                                                                                                                |

All errors are wrapped with `github.com/Laisky/errors/v2` (AGENTS.md) and logged via `gmw.GetLogger(c)` when inside a request path.

### 4.4 MCP tool layer

| #   | File                                                                                                                    | Change                                                                                                                                                                                                                                                                     |
| --- | ----------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| T1  | [internal/mcp/tools/get_user_request.go:286](../../internal/mcp/tools/get_user_request.go#L286) `buildCommandsResponse` | Rewrite: replace `NewToolResultJSON` with manual `CallToolResult` assembly; iterate requests per §3.2; keep a short-circuit pure-text path so the text-only output remains byte-identical.                                                                                 |
| T2  | [internal/mcp/tools/get_user_request.go:66](../../internal/mcp/tools/get_user_request.go#L66) `Definition`              | Add `mcp.WithAnnotation("anthropic/maxResultSizeChars", 20000)` for the text portion; document that the tool may return mixed content.                                                                                                                                     |
| T3  | new helper `internal/mcp/tools/image_budget.go`                                                                         | Extract the §3.8 budget algorithm into a standalone, unit-testable function.                                                                                                                                                                                               |
| T4  | `UserRequestService` interface                                                                                          | Extend so `ConsumeAllPending` / `ConsumeFirstPending` return `[]RequestImage`; inject a `PresignedURLIssuer` abstraction (so tests can substitute a fake) that wraps `storage.MinIO.PresignedGet`.                                                                         |
| T5  | `internal/mcp/tools/get_user_request.go`                                                                                | For images still within §3.8 inline budget, the tool fetches the PNG bytes via MinIO `GetObject` (server-internal, not presigned), base64-encodes, and emits as `ImageContent`. This adds one storage GET per inlined image; mitigated by per-image size ceiling (40 KiB). |

Each new function/interface gets a leading comment starting with its own name (AGENTS.md comment rule).

### 4.5 Frontend

User-visible compose-box contract (see test matrix §6 for exhaustive coverage):

- **Image button** — an icon button in the compose toolbar. Click opens the native file picker (`accept="image/*"`, `multiple`). The button is visibly disabled (greyed + tooltip "Maximum 5 images") when the pending attachment count reaches 5.
- **Drag-and-drop** — the entire compose area is a drop target. Dragging image files over it shows a highlighted outline and "Drop images to attach" overlay. Dropping files adds them up to the 5-image limit; extras trigger a toast "5 image limit reached; extras ignored".
- **Clipboard paste** — `onPaste` on the textarea accepts images from the system clipboard (screenshots).
- **Add-by-URL** — a secondary menu item on the image button: "Attach from URL...". Opens an inline input; `Enter` submits the URL for server-side fetch. The URL occupies one of the 5 slots immediately (with a spinner) and resolves to a thumbnail or an inline error when the server replies.
- **Pending-attachment strip** — rendered **below** the textarea (not inside it). Each attachment is a 48 × 48 px rounded thumbnail (for files, rendered from `URL.createObjectURL`; for URLs, a generic icon until the thumbnail resolves). Long press / focus is accessible via keyboard.
- **Hover delete** — on pointer hover or keyboard focus, an `×` button appears in the top-right corner of each thumbnail. Clicking removes the attachment and re-enables the image button if count drops below 5. Keyboard `Delete`/`Backspace` on a focused thumbnail also removes it.
- **Per-attachment state badges** — small overlay on the thumbnail:
  - pending upload → spinner;
  - server error (quota / MIME / SSRF / fetch failure) → red `!` with the error in a tooltip;
  - success → no badge.
- **Submit button** is enabled when (`content` is non-empty OR at least one successful attachment) AND there is no in-flight attachment. Clicking it also blocks while attachments are still uploading.
- **Quota readout** — always visible near the compose box: "12.3 / 100 MB used, images expire in 7 days". Fetched via `GET /api/quota`, refreshed after each successful upload and after each submit.

| #   | File                                                                                             | Change                                                                                                                                                                                                                                                                                                |
| --- | ------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| F1  | [web/src/features/mcp/user-requests/page.tsx](../../web/src/features/mcp/user-requests/page.tsx) | Compose layout: textarea + toolbar row with the image button, `Attach from URL` menu item, and quota readout; thumbnail strip below. No inline styles (AGENTS.md CSS rule).                                                                                                                          |
| F2  | new component `web/src/features/mcp/user-requests/AttachmentStrip.tsx`                           | Renders the pending-attachment strip with thumbnails, hover/focus delete, and per-attachment status badges. Handles drop target visuals and paste capture.                                                                                                                                           |
| F3  | new component `web/src/features/mcp/user-requests/UrlAttachmentDialog.tsx`                       | Inline URL input that validates scheme, submits the URL to the server via `createUserRequest`, and reports failures with inline error text.                                                                                                                                                          |
| F4  | [web/src/features/mcp/user-requests/api.ts](../../web/src/features/mcp/user-requests/api.ts)     | `createUserRequest(apiKey, { content, taskId, files, urls })`; browser-built `FormData` with `images[]` file parts and `image_urls[]` text parts. Typed error codes: `quota_exceeded`, `image_too_large`, `unsupported_mime`, `too_many_images`, `url_blocked`, `url_fetch_failed`, `decode_failed`. |
| F5  | new module `web/src/features/mcp/user-requests/image-utils.ts`                                   | Canvas-based pre-upload resize (≤ 1536 long edge, JPEG 0.85), EXIF orientation, MIME allowlist. (Still worthwhile — smaller payload, faster server normalize.)                                                                                                                                       |
| F6  | `page.tsx` list rendering                                                                        | Shows submitted images as thumbnails via presigned MinIO URLs; badge when within 24 h of expiry.                                                                                                                                                                                                     |
| F7  | new hook `useQuota()`                                                                            | Polls `GET /api/quota` on mount, after each submit, and after each successful attachment; exposes `usedBytes`, `quotaBytes`, `percent`.                                                                                                                                                              |
| F8  | `page.tsx` keyboard accessibility                                                                | Thumbnails are focusable (`tabIndex=0`); `Delete` / `Backspace` removes the focused attachment; focus returns to the image button after removal.                                                                                                                                                     |

All package operations in `web/` use `pnpm` (AGENTS.md JS rule).

### 4.6 Configuration

| #   | File                                                                       | Change                                                                                                                                                                                                                                                                                           |
| --- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| C1  | [docs/example/config/settings.yml](../../docs/example/config/settings.yml) | Add a `mcp.user_requests.images` section: `enabled`, `minio: {endpoint (default "s3.laisky.com"), bucket, prefix, access_key, secret_key, use_ssl (default true)}`, `per_user_quota_bytes` (default 100 MiB), `per_image_max_bytes` (default 20 MiB), `max_per_request` (default 5), `object_ttl_days` (default 7), `presign_ttl_minutes` (default 30), `url_fetch: {allow_http (default false), max_redirects (default 3), total_timeout_seconds (default 15)}`. |
| C2  | `cmd/config_validation.go`                                                 | Validate that when `enabled=true`, MinIO endpoint/bucket/creds are non-empty and reachable at startup (a single `BucketExists` probe).                                                                                                                                          |
| C3  | `.github/instructions/laisky.instructions.md`                              | Record the real MinIO AK / SK for local debugging only. Do not commit real creds to example YAML.                                                                                                                                                                               |

### 4.7 Documentation

| #    | File                                                 | Change                                                                                           |
| ---- | ---------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| Doc1 | [docs/manual/mcp.md](../../docs/manual/mcp.md)       | New "Image Messages" section: limits, per-client behavior differences, 7-day TTL, 100 MiB quota. |
| Doc2 | [docs/arch/mcp_pipe.md](../../docs/arch/mcp_pipe.md) | Append an image-pipeline subsection covering MinIO storage and the normalize-to-PNG flow.        |

## 5. API Contract

### 5.1 Submit a request (multipart)

```
POST /api/requests
Authorization: Bearer <api-key>
Content-Type: multipart/form-data; boundary=...

--...
Content-Disposition: form-data; name="content"

Please fix the layout issue shown in this screenshot and compare with the reference image.
--...
Content-Disposition: form-data; name="task_id"

default
--...
Content-Disposition: form-data; name="images"; filename="screenshot.jpg"
Content-Type: image/jpeg

<binary>
--...
Content-Disposition: form-data; name="image_urls"

https://example.com/reference.png
--...--
```

A JSON variant is also accepted:

```
POST /api/requests
Authorization: Bearer <api-key>
Content-Type: application/json

{ "content": "...", "task_id": "default", "image_urls": ["https://example.com/a.png", "https://example.com/b.jpg"] }
```

The server fetches each URL via §3.7 and runs the normalization pipeline. Total (files + URLs) must not exceed 5.

Success response (the server converted the screenshot to PNG):

```json
{
  "request": {
    "id": "...",
    "content": "...",
    "task_id": "default",
    "images": [
      {
        "id": "...",
        "sha256": "9f8b...c1",
        "mime": "image/png",
        "url": "https://s3.laisky.com/bucket/prefix/efjkq@pieverse.io/9f8b...c1.png?X-Amz-Algorithm=AWS4-...&X-Amz-Expires=1800&...",
        "size": 45120,
        "width": 1200,
        "height": 800,
        "expires_at": "2026-04-26T03:12:45Z"
      }
    ],
    "status": "pending",
    "created_at": "2026-04-19T03:12:45Z"
  }
}
```

Error responses (all JSON `{"error": code, "message": "...", "attachment_index": N /* optional */}`):

| HTTP | `error`               | Trigger                                                                       |
| ---- | --------------------- | ----------------------------------------------------------------------------- |
| 400  | `unsupported_mime`    | Input MIME not in allowed set, or magic-byte sniff disagrees with hint.       |
| 400  | `url_blocked`         | `image_url` uses a disallowed scheme or resolves to a private / metadata IP.  |
| 413  | `image_too_large`     | Single file / URL body exceeds 20 MiB.                                        |
| 413  | `quota_exceeded`      | Would push the user above 100 MiB (post-normalization total).                 |
| 413  | `too_many_images`     | More than 5 attachments in one request (files and URLs counted together).    |
| 415  | `feature_disabled`    | `images.enabled=false` or flag off.                                           |
| 422  | `decode_failed`       | Image is corrupt or dimensions exceed 8192 px.                                |
| 502  | `url_fetch_failed`    | `image_url` returned non-2xx, TLS failure, or unreadable body.                |
| 504  | `url_timeout`         | `image_url` exceeded the 15 s total fetch deadline.                           |
| 503  | `storage_unavailable` | MinIO PUT failed.                                                             |

`attachment_index` is the 0-based position of the failing attachment in the combined (files + URLs) list; the composer UI uses it to light up the offending thumbnail.

### 5.2 Quota probe

```
GET /api/quota
-> 200
{
  "user_identity": "efjkq@pieverse.io",
  "used_bytes": 12345678,
  "quota_bytes": 104857600,
  "object_count": 37,
  "ttl_days": 7
}
```

### 5.3 Image retrieval

Images are served directly by MinIO at `https://s3.laisky.com/<bucket>/<prefix>/<UserIdentity>/<sha256>.png` with a 30-minute AWS V4 presign. No application endpoint is involved. An unsigned or expired URL returns MinIO's standard 403.

### 5.4 MCP response example (1 request + 1 image, inline budget available)

```json
{
  "content": [
    {
      "type": "text",
      "text": "{\"command\":0,\"content\":\"Check this screenshot\",\"images\":[{\"id\":\"a1\",\"sha256\":\"9f8b...c1\",\"url\":\"https://s3.laisky.com/bucket/prefix/efjkq@pieverse.io/9f8b...c1.png?X-Amz-...\",\"mime\":\"image/png\",\"expires_at\":\"2026-04-26T03:12:45Z\"}]}"
    },
    { "type": "image", "data": "<base64>", "mimeType": "image/png" },
    {
      "type": "resource_link",
      "uri": "https://s3.laisky.com/bucket/prefix/efjkq@pieverse.io/9f8b...c1.png?X-Amz-...",
      "name": "9f8b...c1.png",
      "mimeType": "image/png"
    }
  ],
  "structuredContent": {
    "commands": [
      {
        "content": "Check this screenshot",
        "images": [{ "id": "a1", "sha256": "9f8b...c1", "url": "https://...", "mime": "image/png" }]
      }
    ],
    "protocol_version": "v2"
  }
}
```

## 6. Test Matrix

All Go tests use `github.com/stretchr/testify/require` (AGENTS.md rule). MinIO-backed integration tests use `testcontainers-go` with the `minio/minio` image; unit tests inject a fake `storage.ObjectStore` interface. Frontend UI tests use Vitest + React Testing Library; user-gesture tests use `@testing-library/user-event` to cover realistic click / drop / paste / keyboard flows.

### 6.1 Backend unit — service, imageproc, quota, GC

| #   | Layer     | Scenario                                                                 | Expected                                                                         |
| --- | --------- | ------------------------------------------------------------------------ | -------------------------------------------------------------------------------- |
| U1  | service   | Pure-text request creation                                               | Succeeds, `Images` empty, no MinIO PUT, no refs row                              |
| U2  | service   | Text + 1 image (PNG input)                                               | PUT called once; refs + links rows written; `ConsumeAllPending` returns metadata |
| U3  | service   | Text + 5 images (mix of files and URLs)                                  | Succeeds; 5 refs; 5 links with matching sort order                                |
| U4  | service   | Text + 6 attachments (any mix)                                           | `ErrTooManyImages`, no PUT                                                       |
| U5  | service   | Single file 20.1 MiB                                                     | `ErrImageTooLarge`, no PUT                                                       |
| U6  | service   | Empty content + 1 image                                                  | Succeeds (image-only allowed)                                                    |
| U7  | service   | Empty content + 0 images                                                 | `ErrEmptyRequest` (unchanged from today)                                         |
| U8  | imageproc | Input MIME `image/gif` (single frame)                                    | First frame extracted, converted to PNG, metadata `original_mime="image/gif"`    |
| U9  | imageproc | Input MIME `image/svg+xml`                                               | `ErrUnsupportedMIME` (XSS guard)                                                 |
| U10 | imageproc | Input MIME `image/heic`                                                  | `ErrUnsupportedMIME` (v1 limitation)                                             |
| U11 | imageproc | JPEG with EXIF orientation = 6 (90° rotation)                            | PNG output is visually corrected                                                 |
| U12 | imageproc | 5000×4000 JPEG                                                           | Resized to longest edge 1536; dimensions decrease proportionally                 |
| U13 | imageproc | 9000×9000 input                                                          | `ErrDimensionsTooLarge` (decode-bomb guard)                                      |
| U14 | imageproc | Corrupt JPEG                                                             | `ErrDecodeFailed`                                                                |
| U15 | imageproc | Same logical image uploaded twice as JPEG then as PNG                    | After normalization, same SHA256 (byte-exact PNG)                                |
| U16 | imageproc | 20 MiB JPEG that decompresses to a 4000×3000 RGB                         | Pipeline succeeds; resulting PNG < 10 MiB                                        |
| U17 | quota     | User at 99 MiB uploads an image whose PNG is > 1 MiB                     | `ErrQuotaExceeded`                                                               |
| U18 | quota     | User at 99 MiB re-uploads an existing SHA (no new bytes charged)         | Succeeds; TTL refreshed; no quota change                                         |
| U19 | quota     | Concurrent uploads of different SHAs reaching the quota limit            | Exactly one fails with `ErrQuotaExceeded` (FOR UPDATE lock)                      |
| U20 | quota     | Expired refs do not count against usage                                  | `used_bytes` excludes expired rows                                               |
| U21 | gc        | Refs with `expires_at < now` are deleted                                 | Rows gone; no MinIO deletion attempted                                           |

### 6.2 Backend — `image_url` fetcher and SSRF

| #   | Layer      | Scenario                                                                | Expected                                               |
| --- | ---------- | ----------------------------------------------------------------------- | ------------------------------------------------------ |
| U22 | urlfetch   | HTTPS URL returning a 1 MiB JPEG                                        | Fetch succeeds; bytes match; MIME hint `image/jpeg`    |
| U23 | urlfetch   | HTTP URL (default config `allow_http=false`)                            | `ErrURLBlocked`                                         |
| U24 | urlfetch   | URL whose DNS resolves to `127.0.0.1`                                   | `ErrURLBlocked`                                         |
| U25 | urlfetch   | URL whose DNS resolves to `10.0.0.1`                                    | `ErrURLBlocked`                                         |
| U26 | urlfetch   | URL whose DNS resolves to `169.254.169.254` (EC2 metadata)              | `ErrURLBlocked`                                         |
| U27 | urlfetch   | DNS-rebinding: first `A` record is public, second is `127.0.0.1`        | `DialContext` re-check rejects; `ErrURLBlocked`         |
| U28 | urlfetch   | 4 consecutive redirects                                                 | `ErrURLFetchFailed` (max_redirects=3)                   |
| U29 | urlfetch   | Redirect hop lands on `http://` (when `allow_http=false`)               | `ErrURLBlocked`                                         |
| U30 | urlfetch   | Body declared 5 MiB but streams 25 MiB                                  | `ErrImageTooLarge` at 20 MiB + 1                        |
| U31 | urlfetch   | Server sits on headers for 20 s                                         | `ErrURLTimeout`                                         |
| U32 | urlfetch   | Server returns `text/html` with `<img>`                                 | Magic-byte sniff fails, `ErrUnsupportedMIME`            |
| U33 | urlfetch   | Valid HTTPS image, `Content-Type: application/octet-stream`             | Succeeds because magic bytes say `image/png`            |

### 6.3 Backend — HTTP, storage, MCP tool

| #   | Layer          | Scenario                                                                 | Expected                                                                         |
| --- | -------------- | ------------------------------------------------------------------------ | -------------------------------------------------------------------------------- |
| U34 | http           | Multipart: 2 files + 3 URLs (total 5)                                    | 201; 5 attachments persisted                                                     |
| U35 | http           | Multipart: 3 files + 3 URLs (total 6)                                    | 413 `too_many_images`                                                            |
| U36 | http           | Multipart with empty content but 1 image                                 | Succeeds (image-only message allowed)                                            |
| U37 | http           | JSON variant with `image_urls`                                           | Succeeds; bytes fetched server-side                                              |
| U38 | http           | Existing JSON path with no images                                        | Byte-identical with pre-change behavior (snapshot)                               |
| U39 | http           | Body > 110 MiB                                                           | 413                                                                              |
| U40 | http           | Error payload shape                                                      | Response JSON includes `error`, `message`, and optional `attachment_index`       |
| U41 | http           | Partial failure: file 1 OK, URL 2 returns 404                            | Whole request rejected; nothing persisted; `attachment_index=1`                   |
| U42 | http quota     | `GET /api/quota` for fresh user                                          | 200 `{used_bytes:0, quota_bytes:104857600, object_count:0, ttl_days:7}`          |
| U43 | storage        | PUT on MinIO-down                                                        | `ErrStorageUnavailable`, 503                                                     |
| U44 | storage        | `EnsureLifecycle` called twice                                           | Idempotent; no duplicate rule                                                    |
| U45 | tool budget    | 80 KB budget, 1 image of 50 KB                                           | Image inlined, `remaining=30 KB`                                                 |
| U46 | tool budget    | Two 60 KB images                                                         | First inlined, second link-only                                                  |
| U47 | tool budget    | Three 30 KB images                                                       | First two inlined, third link-only (per-image ceiling)                           |
| U48 | tool           | Pure-text response snapshot                                              | Byte-identical with pre-change version                                           |
| U49 | tool           | One text + one image `CallToolResult`                                    | `Content` order = [text, image, resource_link]; URL is a fresh presign           |
| U50 | tool           | Presign TTL applied                                                      | URL `X-Amz-Expires=1800`                                                         |
| U51 | tool           | Hold mode + images                                                       | Hold release delivers text + images correctly                                    |
| U52 | tool           | `return_mode=first` with multiple images                                 | Only the first request's images are returned                                     |

### 6.4 Frontend — compose-box user behavior

End-to-end behavioral coverage of the compose box. Each row is one user-centric test.

| #   | Scenario                                                                          | Expected                                                                                                                   |
| --- | --------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| F1  | Click the image button → native picker → choose 2 files                           | 2 thumbnails appear below the textarea, in selection order; image button remains enabled (count 2/5)                      |
| F2  | Click the image button, select 6 files in the picker                              | First 5 accepted; 6th dropped with toast "5 image limit reached"; image button becomes disabled                             |
| F3  | Drag 3 files over the compose area                                                | Drop overlay appears ("Drop images to attach"); dropping adds all 3; overlay disappears                                    |
| F4  | Drag a PDF over the compose area                                                  | Overlay shows "Only images accepted"; dropping is rejected with a toast                                                    |
| F5  | Paste a PNG from the system clipboard                                             | One thumbnail appears; attachment count increments                                                                        |
| F6  | Open "Attach from URL", type a valid URL, press Enter                             | Pending thumbnail with spinner; on success shows the fetched image thumbnail                                              |
| F7  | Open "Attach from URL", type `http://127.0.0.1/x.png`, press Enter                | Thumbnail flashes a red `!`; tooltip shows "URL blocked" (`url_blocked`)                                                   |
| F8  | Open "Attach from URL", type a URL the server times out on                        | Thumbnail shows red `!`; tooltip shows "URL fetch timed out"                                                                |
| F9  | Hover a thumbnail                                                                 | `×` delete button fades in within 150 ms                                                                                   |
| F10 | Click the `×` delete button on a thumbnail                                        | Thumbnail removed; image button re-enables if count drops below 5                                                          |
| F11 | Focus a thumbnail with Tab, press `Delete`                                        | Thumbnail removed; focus returns to the image button                                                                       |
| F12 | Attach 5 images, then try drag-drop another file                                  | Overlay shows "5 image limit reached"; drop is ignored                                                                     |
| F13 | Attach the same file twice via the file picker                                    | Both thumbnails appear; server returns one shared SHA on submit (not a UI concern but does not error)                      |
| F14 | Click image button rapidly 10×                                                    | Only one native picker opens (guard with `useRef` flag)                                                                    |
| F15 | Submit while one attachment is still uploading                                    | Submit button is disabled and shows "Waiting for uploads…"; submits only once all resolve                                  |
| F16 | Submit with content empty and no attachments                                      | Submit button is disabled; pressing Enter in textarea does not submit                                                      |
| F17 | Submit with content empty and 1 attachment                                        | Submit allowed; server accepts                                                                                             |
| F18 | Server returns `quota_exceeded`                                                   | Inline banner shows "Quota exceeded: 98.7 / 100 MB used — delete or wait for expiry"; quota readout refreshes               |
| F19 | Server returns `image_too_large` for attachment index 2                           | Thumbnail 2 shows red `!`; other thumbnails unaffected; submit button re-enables                                           |
| F20 | Drop a 25 MiB JPEG                                                                | Client pre-upload check flags `image_too_large` before even sending (pre-resize detects raw size)                          |
| F21 | Paste a 2 MiB screenshot                                                          | Client resizes via canvas; upload payload < 500 KiB                                                                        |
| F22 | Navigate away with pending attachments                                            | Browser confirmation prompt ("You have unsaved images") via `beforeunload`                                                 |
| F23 | Load existing list — submitted request from 6 days ago                            | Thumbnail renders; badge "expires tomorrow" visible                                                                        |
| F24 | Load existing list — submitted request from 8 days ago (MinIO already purged)    | Thumbnail placeholder + "expired" badge; click goes nowhere                                                                |
| F25 | Offline (network down) when pressing Submit                                       | Error toast; content and attachments preserved in the compose box for retry                                                |
| F26 | Accessibility: screen reader on the image button                                  | Announces "Attach image, 2 of 5 attached"                                                                                  |

### 6.5 Integration

| #   | Scenario                                                                 | Expected                                                                       |
| --- | ------------------------------------------------------------------------ | ------------------------------------------------------------------------------ |
| I1  | Real Claude Code call against `https://s3.laisky.com`                    | Agent describes the image accurately in its reply                              |
| I2  | Real Codex CLI (v0.117+) call against `https://s3.laisky.com`            | Agent uses `view_image` to load the presigned URL and describes the image      |
| I3  | Claude Code, `MAX_MCP_OUTPUT_TOKENS=25000`, three images totaling 200 KB | No output warning; oversized images degrade to link-only                       |
| I4  | Codex CLI with network isolation (presign URL unreachable)               | Agent gets the link, reports a clear error, does not panic                     |
| I5  | Real MinIO, 7-day lifecycle rule installed                               | After bootstrap, `GetBucketLifecycle` returns the expected rule                |
| I6  | Older Claude Desktop / MCP Inspector                                     | Mixed content at least surfaces text; image block ignored without crash        |
| I7  | Full-flow: user drags 2 files + 1 URL, submits, agent reads all 3        | All three attachments surface in the MCP response; agent describes each        |

### 6.6 Security

| #   | Scenario                                                                  | Expected                                                                              |
| --- | ------------------------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| S1  | Upload with spoofed MIME (PHP renamed to image/png)                       | Server rejects after decode failure (`decode_failed`)                                 |
| S2  | Cross-tenant upload attempt (API key A submits, `UserIdentity` from auth) | `UserIdentity` from auth is authoritative; payload-supplied identity ignored          |
| S3  | Presigned URL leaked after issuance                                       | Valid for 30 min only; after expiry MinIO returns 403                                 |
| S4  | Presigned URL tampered (signature flipped)                                | MinIO returns 403                                                                     |
| S5  | SQL injection attempt (image filename with `';--`)                        | Parameterized query neutralizes                                                       |
| S6  | Path traversal in `UserIdentity` (e.g. `../`)                             | Sanitization rejects or escapes; object key remains inside the per-user prefix        |
| S7  | SHA256 collision attempt (not practically feasible; sanity test)          | If two distinct bytes somehow produce same key, the second PUT overwrites — OK        |
| S8  | `image_url` pointing at internal admin UI                                 | §3.7 blocks via IP check; request fails with `url_blocked`                            |
| S9  | Content-Disposition smuggling via fetched URL                             | Response headers are ignored; object is always stored with `image/png` Content-Type   |

**Manual smoke suite** (required before each release): I1 + I2 + I7 + U43 + S3 + S8.

## 7. Acceptance Criteria

Release requires all of the following:

- [ ] **Functionality**
  - [ ] All U*, F*, I*, S* tests pass; CI is green.
  - [ ] Pure-text HTTP and MCP responses are byte-identical with pre-change output (U38, U48 snapshots).
  - [ ] In Claude Code, the agent accurately describes attached images (I1 manual verification).
  - [ ] In Codex CLI (v0.117.0+), the agent uses `view_image` to retrieve and describe the image (I2 manual verification).
  - [ ] Any allowed input MIME (JPEG / PNG / WebP / BMP / TIFF / GIF first-frame) round-trips to a readable PNG (U8, U11, U15).
  - [ ] A single request can mix uploaded files and `image_url`s up to the 5-attachment limit (U34, I7).
  - [ ] Compose-box UX tests F1–F26 all pass.
- [ ] **Storage & quota**
  - [ ] Bucket lifecycle rule installed at startup; objects expire after 7 days (I5).
  - [ ] A user exceeding 100 MiB gets a 413 with `quota_exceeded` (U17).
  - [ ] Re-uploading the same SHA does not charge quota but refreshes TTL (U18).
  - [ ] GC job removes expired refs (U21).
  - [ ] `GET /api/quota` returns accurate counts (U42).
- [ ] **Performance and budget**
  - [ ] A response with one request and three images serializes to ≤ 100 KiB; Claude Code shows no output warning.
  - [ ] `buildCommandsResponse` p95 ≤ 100 ms including up to two MinIO GETs for inlined images (presign itself is local signing, < 1 ms).
  - [ ] Upload pipeline p95 ≤ 500 ms for a 2 MiB JPEG (decode + resize + encode + PUT).
- [ ] **Security**
  - [ ] Presigned URLs expire in ≤ 30 minutes; MinIO rejects them afterwards (U50, S3).
  - [ ] Tampered presigns return 403 (S4).
  - [ ] SVG / HEIC / spoofed MIME uploads are rejected (U9, U10, S1).
  - [ ] `UserIdentity` is taken from the auth context, not request payload; path traversal is impossible (S2, S6).
  - [ ] `image_url` fetcher blocks private / loopback / link-local / metadata IPs, including DNS-rebinding attacks (U24–U27, S8).
  - [ ] HTTP (unencrypted) URLs blocked by default (U23).
  - [ ] Fetcher enforces 20 MiB body cap and 15 s timeout (U30, U31).
- [ ] **Documentation**
  - [ ] [docs/manual/mcp.md](../../docs/manual/mcp.md) updated, including per-client behavior differences, quota, and TTL.
  - [ ] This proposal's `status` flips from `proposed` to `accepted` or `shipped`.
- [ ] **Compatibility**
  - [ ] New DB tables are additive; no backfill.
  - [ ] Existing (non-upgraded) frontends can still submit plain text.
  - [ ] The `UserRequestService` interface evolves in a backward-compatible way.
- [ ] **Code quality (AGENTS.md)**
  - [ ] Every new function/interface has a leading comment starting with its own name.
  - [ ] All errors are wrapped with `github.com/Laisky/errors/v2`.
  - [ ] No file exceeds 800 lines (600 for Go) after the change.
  - [ ] No logged secrets (MinIO SK never appears in logs).

## 8. Rollback Plan

- Code level: feature flag `mcp.user_requests.images.enabled` (default `false` for the first week after deploy, then flipped to `true`). When disabled:
  - Frontend hides the upload UI.
  - HTTP multipart requests return `415 feature_disabled`.
  - `buildCommandsResponse` takes the pure-text branch.
  - `GET /api/quota` returns `quota_bytes: 0`.
- Data level: `mcp_user_image_refs` and `mcp_user_request_image_links` tables remain. For a full rollback, run a standalone `DROP TABLE` migration; the main `Request` table is untouched.
- Storage level: MinIO objects already have a 7-day lifecycle and self-cleanup. If we want to purge faster, temporarily set the rule to 1 day; no manual delete needed.
- Observability: new counters
  - `mcp_user_request_images_uploaded_total{status}`
  - `mcp_user_request_images_quota_rejected_total`
  - `mcp_image_budget_overflow_total`
  - `mcp_image_normalize_duration_seconds` (histogram)
  - `mcp_storage_put_errors_total`
    for 48 h post-deploy monitoring.

## 9. Open Questions

1. **Redis-backed quota cache**: at high write rates, `SELECT SUM(...) FOR UPDATE` becomes a bottleneck. Consider caching the per-user `used_bytes` in Redis with atomic `INCRBY` / `DECRBY` once uploads exceed ~5/s per user. Not required for v1.
2. **HEIC / AVIF support**: deferred due to cgo dependency. If demand is high, add a build tag that links `libheif` / `libaom`.
3. **Protocol version evolution**: `structuredContent.protocol_version` uses explicit version tags (`v2`, `v3`, ...); agents choose a parse path per version.
4. **CORS for browser thumbnails**: the web UI will `<img src>` against `https://s3.laisky.com`. Confirm MinIO bucket CORS allows our origin(s), or fall back to proxied thumbnails just for the UI (not for agents).
5. **Interpretation of "session" in the 5-image cap**: the proposal treats the cap as **per `get_user_request` submission** (one HTTP POST). If we later want a per-`task_id` cumulative cap, add a separate counter; flag before implementation.
