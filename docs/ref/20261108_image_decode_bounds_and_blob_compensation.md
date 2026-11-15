# Image decode bounds and object-store compensation (2026-11-08)

Sources verified on 2026-11-08 for issues #83 and #87 (user-request images).
Cited by `internal/mcp/imageproc/budget.go`,
`internal/mcp/userrequests/image_objects.go`, `internal/mcp/storage/minio.go`
and `docs/proposals/get_user_request_image_support.md` §3.9.

## Go image decoders

- `image` package security note: "When operating on arbitrary images,
  DecodeConfig should be called before Decode" —
  https://pkg.go.dev/image#hdr-Security_Considerations
- `image/gif`: `Decode` returns the first embedded image and stops reading
  after it; `DecodeAll` "retains every decoded frame in memory" —
  https://pkg.go.dev/image/gif. Frames larger than the logical screen are
  rejected by the decoder, so the logical screen from `DecodeConfig` bounds
  the first frame.
- Decoders allocate the whole image before reading pixel data
  (`image/png` at the first IDAT, `golang.org/x/image/bmp` before the first
  row, `image/jpeg` at the first scan), so header-only inputs can force the
  full allocation. Measured in `internal/mcp/imageproc/budget_test.go`.
- `golang.org/x/image/draw` kernel scalers (`CatmullRom.Scale`) allocate a
  temporary `make([][4]float64, dw*sh)` per call (32 bytes per element) —
  https://cs.opensource.google/go/x/image/+/master:draw/scale.go. For an
  8192 x 8192 source resized to 1536 that is 384 MiB. `dw*sh` is computed in
  int32; our 1536 x 8192 maximum is far from overflow.

## DEFLATE expansion bound

- zlib technical details: the "theoretical limit for the zlib format ... is
  1032:1" (a length/distance pair needs at least two bits and yields at most
  258 bytes) — https://zlib.net/zlib_tech.html; RFC 1951 confirms the one-bit
  minimum distance code.
- PNG: "The concatenation of the contents of all the IDAT chunks makes up a
  zlib datastream" — https://www.w3.org/TR/png-3/ §10.2. Therefore a valid PNG
  file is at least (declared pixel bytes / 1032) long; image/png reaches about
  885:1 on uniform 16-bit rows.

## Industry pixel limits (for comparison with the 8192 px contract)

- Pillow `Image.MAX_IMAGE_PIXELS` = 89,478,485 (warning above, error above
  2x) — https://pillow.readthedocs.io/en/stable/reference/Image.html
- sharp `limitInputPixels` default 268402689 (0x3FFF x 0x3FFF) —
  https://sharp.pixelplumbing.com/api-constructor
- ImageMagick shipped `websafe`/`secure` policies: 8KP width/height, 32MP
  area, 512-768 MiB memory — https://imagemagick.org/security-policy/
- Conclusion: the 8192 x 8192 (67 MP) contract is within industry pixel
  limits; the risk is bytes per pixel and concurrency, hence a memory-weighted
  admission budget instead of a lower pixel cap.

## Object store semantics

- S3 `PutBucketLifecycleConfiguration` "will overwrite an existing lifecycle
  configuration, so if you want to retain any configuration details, they must
  be included in the new lifecycle configuration" —
  https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketLifecycleConfiguration.html.
  minio-go `SetBucketLifecycle` sends the full body (and an empty config
  deletes all rules), so rules must be read, merged and written back.
- Lifecycle concurrency (verified 2026-11-15, maintainer review of PR #94):
  `PutBucketLifecycleConfiguration` accepts no `If-Match`/ETag/version and
  returns none — https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketLifecycleConfiguration.html;
  S3 conditional writes cover only PutObject, CompleteMultipartUpload and
  CopyObject (and conditional deletes), no bucket subresource —
  https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-requests.html;
  bucket configurations are eventually consistent, last writer wins, and a new
  lifecycle configuration may take "a few minutes" to propagate —
  https://docs.aws.amazon.com/AmazonS3/latest/userguide/how-to-set-lifecycle-configuration-intro.html.
  MinIO's `PutBucketLifecycleHandler` reads no precondition header and
  replaces the whole configuration —
  https://github.com/minio/minio/blob/master/cmd/bucket-lifecycle-handlers.go.
  Terraform's AWS provider allows one lifecycle resource per bucket and polls
  after each PUT until 10 consecutive reads match —
  https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/s3/bucket_lifecycle_configuration.go.
  Measured against MinIO 2025-07 (`bitnamilegacy/minio:2025.7.23`): four
  writers merging rules for different prefixes at once lost 36 of 48 installs
  with a single read-merge-write (and with an immediate read-back, which runs
  before the other writers' PUTs); with a jittered ~3 s settle delay before the
  read-back, 0 of 48. Hence `EnsureLifecycle`'s settle delay and the periodic
  lifecycle check, and the `verify` mode for one owner (IaC) per bucket.
- Deletes of missing keys succeed: `DeleteObjects` "confirms the deletion"
  of keys that are not found
  (https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteObjects.html);
  MinIO's `DeleteObjectHandler` answers 204 for not-found objects.
- A client-side PUT failure is ambiguous: after a timeout the server may have
  stored the object (AWS Builders' Library, "Making retries safe with
  idempotent APIs") — so compensation must include the failing key.

## PostgreSQL advisory locks

- `pg_advisory_xact_lock(bigint)` "Obtains an exclusive transaction-level
  advisory lock, waiting if necessary"; transaction-level locks "are held
  until the current transaction ends; there is no provision for manual
  release" — https://www.postgresql.org/docs/current/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS

## Review follow-ups (2026-11-10)

### Decoder buffers missed by the first estimator

- `golang.org/x/image/tiff`: "Maximum data per pixel is 8 bytes (RGBA64)",
  `blockMaxDataSize := int64(blockWidth) * int64(blockHeight) * 8`; LZW,
  Deflate and CCITT blocks are read with `readBuf(r, d.buf, blockMaxDataSize)`
  into a `bytes.Buffer` (PackBits via `unpackBits` with the same limit) —
  https://raw.githubusercontent.com/golang/image/master/tiff/reader.go. A
  compressed strip may therefore inflate to 8 bytes per pixel of the block
  whatever the bit depth; doubling growth allocates up to ~4x the final size
  in total (measured 3.5x).
- `image/png`: a grayscale image with `tRNS` decodes to `*image.NRGBA`
  (8-bit) or `*image.NRGBA64` (16-bit), but `DecodeConfig` stops after IHDR and
  reports `color.GrayModel` / `color.Gray16Model` —
  https://raw.githubusercontent.com/golang/go/master/src/image/png/reader.go.
- `image/gif`: `uninterlace` copies the frame once more.
- The previous EXIF orientation code allocated up to three RGBA copies and
  boxed one `color.Color` per pixel (`src.At`); the replacement writes one copy
  with typed accessors and is byte-identical.

### Transaction isolation and locks

- PostgreSQL: "a query in a repeatable read transaction sees a snapshot as of
  the start of the first non-transaction-control statement in the
  transaction"; "Read Committed mode starts each command with a new snapshot
  that includes all transactions committed up to that instant" —
  https://www.postgresql.org/docs/current/transaction-iso.html. A transaction
  whose first statement waits on `pg_advisory_xact_lock` under REPEATABLE READ
  therefore cannot see what the previous lock holder committed; image
  transactions pin READ COMMITTED (`sql.TxOptions`). `mattn/go-sqlite3`
  ignores the requested level.

### Claims as a transactional outbox

- Transactional outbox: the service first stores the message in its database
  in the same transaction as the business data; a separate process forwards it
  — https://microservices.io/patterns/data/transactional-outbox.html. Image
  object claims follow the same idea for blob writes and deletes: the intent is
  committed (under the scope lock) before the object store is touched, and the
  reconciler acts only on recorded intents, never on a bucket listing.

### Object store permissions (S3 / MinIO action names)

- Mapping table — https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-with-s3-policy-actions.html:
  HeadBucket requires `s3:ListBucket`; DeleteObject `s3:DeleteObject`;
  GetBucketLocation `s3:GetBucketLocation`; PutObject `s3:PutObject`;
  GetObject `s3:GetObject`. Lifecycle: `s3:GetLifecycleConfiguration` and
  `s3:PutLifecycleConfiguration` (API pages
  https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketLifecycleConfiguration.html,
  https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketLifecycleConfiguration.html).
- Presigned URLs carry the signer's permissions —
  https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-presigned-url.html.
- MinIO uses the same action names —
  https://docs.min.io/enterprise/aistor-object-store/administration/iam/access/;
  its HeadBucket handler accepts `s3:ListBucket`.
- minio-go's lifecycle model keeps one `Transition` per rule and drops
  unknown elements, so a read-modify-write can silently change other
  applications' rules; the configuration is now read raw and compared as an
  order-insensitive XML tree before any write.

## Second review follow-ups (2026-11-15)

### Abandoned PUTs and late writes

- S3 never stores a partial object, but documents nothing about a request the
  client abandoned and gives no bound on when such a write becomes visible.
  Concurrent writes to one key are "latest timestamp wins", and the order in
  which S3 receives them "cannot be predicted"; a DELETE sent while an earlier
  PUT is unacknowledged is therefore concurrent with it —
  https://docs.aws.amazon.com/AmazonS3/latest/userguide/Welcome.html#ConsistencyModel,
  https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObject.html.
- MinIO claims strict read-after-write consistency (on xfs/zfs/btrfs) and
  documents nothing about abandoned PUTs either —
  https://github.com/minio/minio/blob/master/docs/distributed/README.md.
- minio-go v7 retries up to `MaxRetry = 10` attempts (200 ms–1 s jittered
  backoff) for seekable bodies, on 408/429/499/5xx, S3 `RequestTimeout`,
  `InternalError`, `SlowDown`, and transport errors while the caller's context
  is alive (`retry.go`, `api.go`). Every abandoned attempt may still commit.
- Consequence for this repository: compensation cannot treat a key as settled
  after its DELETE succeeds. Every key whose PUT started gets an orphan claim
  due after a 5-minute late-write window; later writes are the documented
  residual window, backed by the lifecycle rule. Conditional writes
  (`If-None-Match`) do not help: content-addressed keys must be overwritable.

### Lifecycle rule IDs

- A rule ID is at most 255 characters and identifies the rule; a
  configuration holds at most 1,000 rules —
  https://docs.aws.amazon.com/AmazonS3/latest/API/API_LifecycleRule.html.
- MinIO (`internal/bucket/lifecycle`) rejects IDs over 255 bytes and duplicate
  IDs ("Rule ID must be unique"), both as 400 `InvalidArgument`.
- The earlier ID `mcp-images-<sanitized prefix>-<days>d` collided for prefixes
  that sanitize alike (`mcp/images/`, `mcp:images/`) and exceeded 255 bytes for
  long prefixes. IDs now embed a digest of the full prefix and are bounded;
  ownership also requires the rule's filter prefix to equal the prefix.

### Industry decode limits (re-checked)

- Pillow: `MAX_IMAGE_PIXELS` = 89,478,485; above 2x it raises
  `DecompressionBombError`, checked against the header in `Image.open()` —
  https://pillow.readthedocs.io/en/stable/handbook/security.html.
- sharp: `limitInputPixels` defaults to 268,402,689 (0x3FFF²) —
  https://sharp.pixelplumbing.com/api-constructor.
- libvips: no pixel-count limit; `VIPS_MAX_COORD` caps each dimension.
- ImageMagick: limits are open by default; `policy.xml` accounts memory, map,
  disk and area budgets, the example policy uses 512 MiB memory, 32 MP area,
  8 KP width/height — https://imagemagick.org/security-policy/.
- All but ImageMagick bound pixel counts, not memory. The 8192 x 8192
  contract (67 MP) sits below Pillow's warning threshold; the per-format
  estimator here is finer-grained, at the cost of tracking decoder changes,
  which the allocation sweep test guards.

### golang.org/x/image/webp (v0.46) allocations

- `vp8.parseOtherPartitions` rejects a final partition of 16 MiB or more
  (golang/go#10790) but then allocates the whole remaining declared chunk
  before reading it, up to about 8 x 16 MiB, whatever the image dimensions.
  A VP8 chunk longer than the file can never decode, so it is now rejected
  before decoding; a valid one is charged its declared length.
- An extended (VP8X) file reports `YCbCr` from `DecodeConfig` unless its alpha
  flag is set, even when its image chunk is lossless VP8L.
- VP8L decodes to NRGBA; inverse color indexing allocates a full 4 B/px copy
  (the bundled input is 2 B/px at most when two pixels share a byte), and the
  decoder accepts up to 2600 Huffman groups (`maxHuffImageSize`, added for
  GO-2026-6222 / CVE-2026-46603, fixed in v0.45.0), whose tables can reach
  ~193 MiB independent of the image size.
- 2026 advisories for the package, all fixed at or before the pinned
  v0.46.0: GO-2026-4961 (VP8X canvas panic, v0.42.0), GO-2026-5061 (VP8
  dimension mismatch panic, v0.43.0), GO-2026-6222 (VP8L Huffman groups,
  v0.45.0) — https://pkg.go.dev/search?q=golang.org%2Fx%2Fimage&m=vuln.

### Go image/jpeg

- `image/jpeg` handles an APP14 (Adobe) segment wherever it appears before
  EOI, including between progressive scans, and converts three-component
  planes to RGBA when the transform is "unknown" (`isRGB`) — Go source
  `src/image/jpeg/reader.go`. A marker scan that stops at the first SOS misses
  that 4 B/px copy.

### Bounding the request body read (Go net/http)

- `http.ResponseController.SetReadDeadline` (Go 1.20+, `src/net/http/responsecontroller.go`)
  "sets the deadline for reading the entire request, including the body";
  reads after it return an error. It reaches the connection through any
  writer that implements `Unwrap() http.ResponseWriter` (gin's
  `responseWriter` does) and returns `ErrNotSupported` otherwise (for
  example `httptest.ResponseRecorder`).
- Request body reads do not observe the request context, and the API server
  sets only `ReadHeaderTimeout`, so a handler that holds a scarce slot while
  reading a body must set this deadline itself.
- `multipart.Reader.ReadForm(maxMemory)` keeps up to `maxMemory` bytes of
  file parts in memory plus a reserved 10 MiB for non-file values
  (`src/mime/multipart/formdata.go`), so the per-request in-memory bound for
  `maxMultipartMemory = 8 MiB` is 18 MiB; file parts beyond it spill to
  temporary files.
