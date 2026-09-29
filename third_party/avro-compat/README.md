# Legacy goar Avro compatibility

`goar v1.6.3` imports the abandoned `github.com/hamba/avro` v1 API in
`utils/tags.go`. GO-2023-1930 has no fixed v1 release. This adapter replaces
that implementation with `github.com/hamba/avro/v2 v2.31.0`, which includes
the decoder fix (first shipped in v2.13.0). The v2 project is also now archived;
this is a narrowly scoped migration to its final fixed release, not a claim
of ongoing upstream maintenance.

Only `Schema`, `Parse`, `Marshal`, and `Unmarshal` are exposed. All behavior
comes from the v2 module, verified by Go's checksum database. No vulnerable
v1 implementation is vendored, renamed, or retained. Remove this adapter
when goar migrates its import path. Root Arweave tests check the real goar
entry points against the independent goavro codec and reject malformed data.
