# Arweave upload protocol references (2026-11-15)

`library/db/arweave.Ardrive` uploads format-2 data transactions. It runs
`GET /price/{size}`, `GET /tx_anchor`, then signs the transaction and sends
`POST /tx` with the header only. After that it sends `POST /chunk` once per
chunk. These facts were checked against the Arweave node source
(master, 2026-09; release N.2.9.8), arweave-js 2.1.0 and `permadao/goar` v1.2.0.

## Why the service owns the HTTP transport

- **goar has no cancellation or timeout.** goar v1.2.0 is the latest release.
  Its `Client` sends every request through `http.DefaultClient`, which has no
  timeout. The client field is unexported, so a custom `*http.Client` cannot be
  injected, and no call takes a `context.Context`.
- **goar's timeout setter changes global state.** Without a proxy,
  `Client.SetTimeout` changes the process-wide `http.DefaultClient`.
- **goar's concurrent upload hides failures.** `ConcurrentOnce(ctx)` retries
  every non-200 forever, fatal errors included. It returns `nil` after
  cancellation, so the caller cannot tell a canceled upload from a finished
  one.
- **No alternative.** `everFinance/goar` and `liteseed/goar` are archived, and
  no maintained Go SDK with context support was found.
- **The split.** goar still builds the transaction: `utils.PrepareChunks`
  computes the chunks and data root, `Signer.SignTx` signs it, and
  `utils.GetChunk` builds each chunk body. `gateway.go` owns the requests:
  every attempt gets its own timeout through `http.NewRequestWithContext`, and
  retries are bounded.

## Commit point and cancellation

- **The reward is spent when the header is mined.** A node deducts it whether
  or not the chunks ever arrive (`ar_node_utils:update_sender_balance`).
  Abandoning the chunks after `POST /tx` therefore pays for data that is never
  stored.
- **Upload's cancellation rule.** Cancelling the caller's context stops Upload
  only until `POST /tx` is sent. From then on Upload runs under
  `context.WithoutCancel` plus a 30-minute bound (`committedUploadTimeout`).
  This matches the old behavior, where both goar versions ignored the context
  entirely.
- **Why 30 minutes.** A node drops an unmined data root from its disk pool
  after `data_root_expiration_time`, which defaults to 30 minutes. Once the
  transaction is mined, chunks are accepted at any time.
- **The paid path detaches too.** The OneAPI upload charge is single-phase and
  never refunded. `telegram/dao.Upload.UploadFileWithApikey` therefore detaches
  the upload from caller cancellation once the charge has been accepted.
  Unbilled uploads keep the caller's cancellation.
- **A returned ID means accepted, not confirmed.** Upload returns the
  transaction ID once the gateway has accepted the signed header and every
  chunk. The transaction may still be pending, and an unmined transaction can
  be dropped. Callers that persist the ID as proof of permanent storage must
  poll `GET /tx/{id}/status` for confirmations; nothing in this repository
  does that today, which predates the goar migration.

## Gateway responses

- **`POST /tx`.** 2xx is success. A resent transaction gets
  `208 Transaction already processed.` for ten minutes after it was accepted.
  After that it gets 400 with `Transaction is already in the mempool.` or
  `Transaction is already on the weave.` The ID is fixed by the signature, so
  all three mean the transaction was sent. arweave-js and goar treat the 400s
  as failures.
- **`POST /chunk` success.** 200 with an empty body, or 303 when the node
  accepted the chunk only temporarily.
- **Fatal chunk errors, never retried.** 400 with `invalid_json`,
  `chunk_too_big`, `data_path_too_big`, `offset_too_big`, `data_size_too_big`,
  `chunk_proof_ratio_not_attractive`, `invalid_proof` or `disk_full`. This is
  goar's `schema.FATAL_CHUNK_UPLOAD_ERRORS`, which is the arweave-js list plus
  `disk_full`.
- **Transient chunk errors, retried.** Any other 400, such as
  `data_root_not_found` or `exceeds_disk_pool_size_limit`. Also 429, and 503
  with `timeout` or `not_joined`. A body over 750000 bytes gets a plain-text
  413, which is fatal.
- **Retry policy.** Each request makes at most 5 attempts. Backoff starts at
  1 s and doubles up to 30 s, honoring a longer `Retry-After`. arweave-js
  instead waits 40 s and allows up to 100 consecutive errors.
- **Chunk validation on a node.** The data root and size must belong to a
  pending or confirmed transaction. The chunk and the `data_path` are each at
  most 256 KiB. The Merkle path must prove `sha256(chunk)` at `offset`, and the
  chunk length must equal the leaf range. `offset` may be any byte in the
  chunk; arweave-js and goar send the inclusive end offset.
- **Bad splits are dropped silently.** A node answers 200 to a chunk with a
  non-canonical split, then drops it later (`disk_pool_chunk_from_bad_split`).
  The test gateway in `fakegateway_test.go` therefore insists on the canonical
  arweave-js split. That split uses 256 KiB chunks, except that a final
  remainder under 32 KiB is balanced with the chunk before it.

## Not adopted

- **Data in the `/tx` body.** arweave-js sends the data of a single-chunk
  transaction in the `/tx` body (`MAX_CHUNKS_IN_BODY = 1`). goar does not, and
  this service keeps goar's wire format.

## Sources

- [ar_http_iface_middleware.erl](https://github.com/ArweaveTeam/arweave/blob/master/apps/arweave/src/ar_http_iface_middleware.erl),
  [ar_disk_pool.erl](https://github.com/ArweaveTeam/arweave/blob/master/apps/arweave/src/ar_disk_pool.erl),
  [ar_merkle.erl](https://github.com/ArweaveTeam/arweave/blob/master/apps/arweave/src/ar_merkle.erl),
  [ar_node_utils.erl](https://github.com/ArweaveTeam/arweave/blob/master/apps/arweave/src/ar_node_utils.erl),
  [ar_ignore_registry.erl](https://github.com/ArweaveTeam/arweave/blob/master/apps/arweave/src/ar_ignore_registry.erl)
- [Arweave HTTP API](https://docs.arweave.org/developers/arweave-node-server/http-api)
- [arweave-js transaction-uploader.ts](https://github.com/ArweaveTeam/arweave-js/blob/master/src/common/lib/transaction-uploader.ts)
  and [merkle.ts](https://github.com/ArweaveTeam/arweave-js/blob/master/src/common/lib/merkle.ts)
- [permadao/goar v1.2.0](https://github.com/permadao/goar/tree/v1.2.0)
- [context.WithoutCancel](https://pkg.go.dev/context#WithoutCancel),
  [http.NewRequestWithContext](https://pkg.go.dev/net/http#NewRequestWithContext)
