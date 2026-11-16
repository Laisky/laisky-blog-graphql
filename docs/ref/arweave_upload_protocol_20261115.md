# Arweave upload protocol references (2026-11-15)

`library/db/arweave.Ardrive` uploads format-2 data transactions. It runs
`GET /price/{size}`, `GET /tx_anchor`, then signs the transaction and sends
`POST /tx` with the header only. After that it sends `POST /chunk` once per
chunk. These facts were checked against the Arweave node source
(master, 2026-09; release N.2.9.8), arweave-js 2.1.0 and `permadao/goar` v1.2.0.
The split, redirect and retry facts were rechecked on 2026-11-16 against
node master `18d402c`, arweave-js master `af2073a`, ar-io-node `bb38caa` and
Go 1.27.4.

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
- **Redirects.** Go's client re-sends a POST that gets a 301, 302 or 303 as a
  bodiless GET of the `Location`, and re-sends it to the `Location` on a 307
  or 308. A 200 there would pass for an accepted transaction or chunk, so
  `checkGatewayRedirect` returns a redirected POST unfollowed
  (`http.ErrUseLastResponse`). `gatewayClient.do` then fails the request for
  good on any 3xx with a `Location`, without retrying it. A node never sends
  a 3xx to `POST /tx`, and its 303 to a chunk has no `Location`. Reads
  (`/price`, `/tx_anchor`) still follow up to 10 redirects, as goar's client
  did. ar-io-node's Envoy resolves its own redirects for these routes, and no
  client-visible redirect is documented.

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
- **`POST /chunk` success.** 200 with an empty body, or a 303 with an empty
  body and no `Location`. The node sends the 303 for `temporary`: it validated
  the chunk and stored it in its disk pool, but none of its storage modules
  covers that offset (`ar_http_iface_middleware.erl`, `ar_disk_pool.erl`).
  ar-io-node also counts 200 and 303 as accepted. arweave-js advances only on
  200, so it re-posts such a chunk.
- **Fatal chunk errors, never retried.** 400 with `invalid_json`,
  `chunk_too_big`, `data_path_too_big`, `offset_too_big`, `data_size_too_big`,
  `chunk_proof_ratio_not_attractive`, `invalid_proof` or `disk_full`. This is
  goar's `schema.FATAL_CHUNK_UPLOAD_ERRORS`, which is the arweave-js list plus
  `disk_full`.
- **Transient chunk errors, retried.** Any other 400, such as
  `data_root_not_found` or `exceeds_disk_pool_size_limit`. Also 429, and 503
  with `timeout` or `not_joined`. A body over 750000 bytes gets a plain-text
  413, which is fatal.
- **Retry policy.** A read or `POST /tx` makes at most 5 attempts. Before the
  header is accepted, the reward is most likely not spent, so an outage is
  reported quickly. A chunk is retried until `committedUploadTimeout`, which
  is 30 minutes from the first `POST /tx`. Only the node that accepted the
  header knows the data root at once. Other nodes learn it when they fetch
  the transaction, and the source sets no bound on how long that takes, so
  `data_root_not_found` can last a while. The root expires 30 minutes after
  a node first sees it (`DEFAULT_DISK_POOL_DATA_ROOT_EXPIRATION_TIME_S`), so
  retrying longer would not help. Backoff starts at 1 s and doubles up to
  30 s. Each delay is drawn from the upper half of the backoff ("equal
  jitter"), so uploads that fail together do not retry in lockstep. A longer
  `Retry-After` is honored, up to 30 s. arweave-js waits 40 s minus up to 30%
  jitter and gives up after 100 consecutive errors, about an hour. A fatal
  chunk error or an unfollowed redirect still fails at once.
- **Chunk validation on a node.** The data root and size must belong to a
  pending or confirmed transaction. The chunk and the `data_path` are each at
  most 256 KiB. The Merkle path must prove `sha256(chunk)` at `offset`, and the
  chunk length must equal the leaf range. `offset` may be any byte in the
  chunk; arweave-js and goar send the inclusive end offset.
- **Bad splits are dropped silently.** At admission, a node accepts a chunk
  that passes any of its three rulesets (base, strict split, offset rebase)
  and records which ones it passed (`ar_poa:validate_data_path2`). When the
  transaction is mined, `ar_disk_pool:validate_for_final_offset` picks the
  ruleset that applies at the chunk's absolute offset. A chunk that fails it
  is dropped with `disk_pool_chunk_from_bad_split`, after the node has
  already answered 200. The weave is past both the strict-split and the
  rebase thresholds, so new data must pass the rebase ruleset.
- **The split rules.** These are the leaf checks of `ar_merkle:validate_path`.
  Under both rulesets, a chunk is at most 256 KiB.
  - Strict split: a full chunk starts on a 256 KiB boundary. The last chunk
    ends off a boundary and starts no later than the last boundary before its
    end. Any other chunk starts on a boundary and has more than 256 KiB but
    less than 512 KiB of the data left.
  - Rebase: the right-most chunk starts on a boundary or steps into exactly
    the next 256 KiB bucket without ending on a boundary. Any other chunk
    starts on a boundary.
- **goar's split differs from arweave-js's and is valid.** Both split
  256 KiB chunks and balance a final remainder under 32 KiB with the chunk
  before it. For an odd balanced stretch, arweave-js (`Math.ceil`) puts the
  larger half first, and goar (integer division before `math.Ceil`) puts it
  last. For 262,145 bytes, arweave-js gives 131,073 + 131,072 and goar gives
  131,072 + 131,073. Both pass both rulesets, as does any split point inside
  the stretch and the node's own `ar_tx:chunk_binary` (256 KiB + 1 byte).
  goar is unchanged on `develop`, and no upstream issue exists.
- **The test gateway applies those rules.** `fakegateway_test.go` rejects a
  chunk that fails either the strict or the rebase rule
  (`nodeSplitRules`). That is stricter than admission and matches the outcome
  after mining. `split_test.go` checks goar's split for sizes around every
  boundary. It also shows that layouts which leave the 256 KiB grid are
  rejected even when their Merkle proofs are valid.

## Not adopted

- **Data in the `/tx` body.** arweave-js sends the data of a single-chunk
  transaction in the `/tx` body (`MAX_CHUNKS_IN_BODY = 1`). goar does not, and
  this service keeps goar's wire format.
- **Our own chunker matching arweave-js byte for byte.** It was considered
  because goar's split differs for odd balanced remainders. The node accepts
  goar's split, so owning the Merkle code would add maintenance for no change
  on the network.
- **Waiting for confirmation, and a persisted upload lifecycle.** A returned
  ID is still "accepted", not "mined" (see above). Polling
  `GET /tx/{id}/status`, or recording charged, sent, accepted and confirmed
  states so that a failed paid upload can be retried or refunded, needs its
  own design. That is separate from this transport.

## Sources

- [ar_merkle.erl leaf checks at 18d402c](https://github.com/ArweaveTeam/arweave/blob/18d402c714132e1de3962b12ff32a420127e0c02/apps/arweave/src/ar_merkle.erl#L131-L195),
  [ar_poa.erl ruleset choice](https://github.com/ArweaveTeam/arweave/blob/18d402c714132e1de3962b12ff32a420127e0c02/apps/arweave/src/ar_poa.erl#L33-L45),
  [ar_disk_pool.erl validate_for_final_offset](https://github.com/ArweaveTeam/arweave/blob/18d402c714132e1de3962b12ff32a420127e0c02/apps/arweave/src/ar_disk_pool.erl#L765-L792)
- [ar_http_iface_middleware.erl](https://github.com/ArweaveTeam/arweave/blob/master/apps/arweave/src/ar_http_iface_middleware.erl),
  [ar_disk_pool.erl](https://github.com/ArweaveTeam/arweave/blob/master/apps/arweave/src/ar_disk_pool.erl),
  [ar_merkle.erl](https://github.com/ArweaveTeam/arweave/blob/master/apps/arweave/src/ar_merkle.erl),
  [ar_node_utils.erl](https://github.com/ArweaveTeam/arweave/blob/master/apps/arweave/src/ar_node_utils.erl),
  [ar_ignore_registry.erl](https://github.com/ArweaveTeam/arweave/blob/master/apps/arweave/src/ar_ignore_registry.erl)
- [Arweave HTTP API](https://docs.arweave.org/developers/arweave-node-server/http-api)
- [arweave-js transaction-uploader.ts](https://github.com/ArweaveTeam/arweave-js/blob/master/src/common/lib/transaction-uploader.ts)
  and [merkle.ts](https://github.com/ArweaveTeam/arweave-js/blob/master/src/common/lib/merkle.ts)
- [permadao/goar v1.2.0](https://github.com/permadao/goar/tree/v1.2.0)
  ([chunk split](https://github.com/permadao/goar/blob/v1.2.0/utils/merkle.go))
- [ar-io-node chunk handler](https://github.com/ar-io/ar-io-node/blob/main/src/arweave/composite-client.ts)
  (accepts 200 and 303)
- [net/http Client redirects](https://pkg.go.dev/net/http#Client.Do)
- [context.WithoutCancel](https://pkg.go.dev/context#WithoutCancel),
  [http.NewRequestWithContext](https://pkg.go.dev/net/http#NewRequestWithContext)
