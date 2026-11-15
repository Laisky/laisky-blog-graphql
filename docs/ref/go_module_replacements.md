# Go module replacements

As of 2026-11-15 the root `go.mod` has no `replace` directives and the
repository has no `third_party/` Go modules. This note records why the two
former replacements existed and why they were dropped rather than upstreamed.

## `github.com/pgvector/pgvector-go`

- **Why it existed.** `third_party/pgvector-go` (added in February 2026, when
  most packages moved from GORM to `database/sql`) kept only `Vector`. Upstream
  v0.3.0's `go.mod` required ent, bun, go-pg, gorm, sqlx, lib/pq and pgx. GORM
  itself stays a direct dependency (the OneAPI user store in
  `internal/web/blog/oneapi` and its wiring in `cmd/api.go` still use it), so
  the concern was the other ORM and driver modules that upstream would have
  added to the module graph.
- **Why it was dropped.** Upstream v0.4.0 and later have a `go.mod` with no
  requirements (v0.4.1, 2026-07-30), so depending on it adds no modules.
- **Behavior differences from the stub.** Upstream `Scan` accepts only `string`
  and `[]byte`; callers handle SQL `NULL` before calling it. Upstream
  `NewVector`/`Slice` share the backing slice instead of copying; every call
  site passes a fresh slice or only reads. `Vector` fields appear only in
  database row structs, so upstream's `MarshalJSON` does not change any API.

## `github.com/hamba/avro` (pulled in by goar)

- **Why it existed.** `everFinance/goar v1.6.3` imports the retired
  `hamba/avro` v1 in `utils/tags.go` (GO-2023-1930, no fixed v1; hamba v2 has
  unfixed GO-2026-5046/5047/5048). The adapter redirected that import to
  `iskorotkov/avro/v2` to keep the imported-package vulnerability scan clean.
- **It was never on a runtime path.** The hamba-backed helpers
  (`SerializeTags1`/`DeserializeTags1`) are not called anywhere in goar, whose
  bundle code uses `linkedin/goavro`. This repository's upload path (then
  `Wallet.SendData`, now `utils.TagsEncode` plus signing) sends base64url JSON
  tags, not Avro.
- **Why it was dropped.** `hamba/avro` and `everFinance/goar` are archived; the
  everFinance README points to `github.com/permadao/goar`, the maintained
  successor (v1.2.0, 2026-11-11), whose tag codec uses only goavro.
- **API delta.** Import path changes and the `types` package is now `schema`.
  The wallet, client and uploader upload path is unchanged apart from these
  renames; signature data omits the owner only for ECDSA signers.
- **Transport now owned locally.** Neither goar version can cancel or time out
  a request (`http.DefaultClient`, no context). `Ardrive` therefore keeps goar
  for signing and chunking only and sends the requests itself; see
  `docs/ref/arweave_upload_protocol_20261115.md`.
- **Known upstream behavior.** permadao `utils.DeserializeTags` ignores bytes
  after the encoded tag array. This repository never decodes Avro tags.

## Acceptance

`library/db/arweave/ardrive_test.go` uploads against a local Arweave gateway.
It verifies the posted transaction fields, Content-Type tag, gzip handling and
gateway rejection, and checks the signature with an independent RSA-PSS and
deep-hash implementation. The wire-contract tests passed unchanged with goar's
`Wallet.SendData` on `everFinance/goar v1.6.3` before the switch and on
`permadao/goar v1.2.0` after it.

The gateway (`fakegateway_test.go`) validates each chunk independently of goar,
as a node does: the Merkle proof against the signed data root, the offset, the
size limits and the canonical split. `TestFakeGatewayRejectsInvalidChunks` is
its negative control. The chunk tests also passed with `Wallet.SendData`
before the transport moved into this repository; the cancellation, timeout and
retry tests in `ardrive_lifecycle_test.go` failed or hung on it.

## If a replacement is ever reintroduced

- Record the reason, the upstream issue or release that would remove it, and
  the removal condition here.
- The Docker build runs `go mod download` before copying the source tree, so
  the replacement's `go.mod`/`go.sum` must be copied explicitly beforehand.

## References

- [pgvector-go releases](https://github.com/pgvector/pgvector-go/tags)
- [permadao/goar](https://github.com/permadao/goar)
- [everFinance/goar (archived)](https://github.com/everFinance/goar)
- [hamba/avro (archived)](https://github.com/hamba/avro)
- [GO-2023-1930](https://pkg.go.dev/vuln/GO-2023-1930)
