# Legacy goar Avro compatibility

`goar v1.6.3` imports the retired `github.com/hamba/avro` v1 API in
`utils/tags.go`. GO-2023-1930 has no fixed v1 release. The final hamba v2.31.0
release also has unfixed GO-2026-5046/5047/5048 findings. This adapter replaces
the decoder with maintained `github.com/iskorotkov/avro/v2 v2.34.0`, which
contains the fixes first shipped in v2.33.0.

Only `Schema`, `Parse`, `Marshal`, and `Unmarshal` are exposed. All behavior
comes from the pinned maintained module, verified by Go's checksum database.
No vulnerable hamba implementation is vendored, renamed, or retained. Remove
this adapter when goar migrates its import path. Root Arweave tests exercise
the real goar entry points against independent goavro encoding, including
malformed lengths and truncated blocks. The imported-package vulnerability
scan remains enabled; no advisory is suppressed.

References: https://pkg.go.dev/vuln/GO-2026-5046,
https://pkg.go.dev/vuln/GO-2026-5047, https://pkg.go.dev/vuln/GO-2026-5048,
and https://github.com/iskorotkov/avro/releases/tag/v2.34.0.
