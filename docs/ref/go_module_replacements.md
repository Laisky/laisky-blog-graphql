# Go module replacements

As of 2026-11-15 the root `go.mod` has no `replace` directives and the
repository has no `third_party/` Go modules. This note records why the two
former replacements existed and why they were dropped rather than upstreamed.

## `github.com/pgvector/pgvector-go`

- **Why it existed.** `third_party/pgvector-go` (added in February 2026 with the
  gorm removal) kept only `Vector`. Upstream v0.3.0's `go.mod` required ent,
  bun, go-pg, gorm, sqlx, lib/pq and pgx, which would have pulled the removed
  ORM stack back into the module graph.
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
  bundle code uses `linkedin/goavro`. This repository only calls
  `Wallet.SendData`, which sends base64url JSON tags, not Avro.
- **Why it was dropped.** `hamba/avro` and `everFinance/goar` are archived; the
  everFinance README points to `github.com/permadao/goar`, the maintained
  successor (v1.2.0, 2026-11-11), whose tag codec uses only goavro.
- **API delta.** Import path changes and the `types` package is now `schema`.
  The wallet, client and uploader upload path is unchanged apart from these
  renames; signature data omits the owner only for ECDSA signers.
- **Known upstream behavior.** permadao `utils.DeserializeTags` ignores bytes
  after the encoded tag array. This repository never decodes Avro tags.

## Acceptance

`library/db/arweave/ardrive_test.go` runs the real goar wallet against a local
Arweave gateway. It verifies the posted transaction fields, Content-Type tag,
chunked payload reassembly, gzip handling and gateway rejection, and checks the
signature with an independent RSA-PSS and deep-hash implementation. The suite
passed unchanged on `everFinance/goar v1.6.3` before the switch and on
`permadao/goar v1.2.0` after it.

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
