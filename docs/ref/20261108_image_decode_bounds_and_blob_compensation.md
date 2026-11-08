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
