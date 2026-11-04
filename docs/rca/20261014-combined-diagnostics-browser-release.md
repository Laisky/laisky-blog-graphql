# Combined diagnostics and browser release qualification

The recurrent `web_search failed` alert lost fields in the application adapter; the old SDK default independently omitted worker fields. The accepted backend repairs are GraphQL8ff3513 and Ramjet8e5fc7b, using SDK261c427b. The original provider/network/parser/authentication cause and incident-time image are not established by the empty alert.

## Source composition

The isolated release sources combine accepted backend releases with Sentry GraphQLbb8a6179 and Ramjetb78fcdcd. Accepted backend delta files and modes are unchanged; each entire frontend tree equals its pinned Sentry PR head. New files add retained socket/service-path regressions only. Owner branches, dependencies, security settings and unrelated holds remain preserved.

## Retained integration

GraphQL `TestMCPWebFetchServicePath` connects actual MCP, billing and alert HTTP sockets through owned real Redis to a separately compiled Ramjet `TestCrawlerServicePathWorker`. It covers HTML/Markdown success, harmless versus active challenge structures, render failure, missing/forged receipt rejection, billing denial, private admission, missing bearer, verified/legacy queue separation, TTL, charged audit semantics and private-content exclusion. Producer and worker structured fields are decoded from actual SDK HTTP requests; a test-only metadata-drop negative control must fail.

Compile the sibling tasks package with `go test -race -c -o /tmp/crawler-service.test ./internal/tasks/gptchat/tasks`, then run GraphQL with `CRAWLER_SERVICE_TEST_WORKER_BINARY=/tmp/crawler-service.test go test -race -count=1 -timeout=3m ./internal/mcp -run '^TestMCPWebFetchServicePath$'`. Existing Speech native library variables apply to the worker build. Missing helper/Redis is incomplete acceptance, not a substitute fake Redis pass.

Chrome and peer identity use synthetic renderer dependencies; call-log persistence is a recorder, and persistence exercises the Redis payload writer instead of its logging wrapper. These tests do not prove an unmodified shipped binary serving a paid production request or real public peers. Existing real Chrome fixtures remain separate evidence. Production paid replay is prohibited.

Ramjet `TestStrictSSOSessionSocketRetained` exercises the production verifier against a signature-validating HTTP authority fixture. GraphQL `TestMongoSSO(Login|Passkey)SocketRetained` extends the real Mongo/GraphQL/controller stack to TCP for legacy password/passkey identity paths and rejection controls. It requires disposable `MONGO_QUERY_TEST_URI`; a skip is incomplete runtime acceptance. These are same-process/synthetic-authority tests, not cross-process GraphQL-to-Ramjet SSO proof.

## Candidate artifact and rollout

Both SPAs are served from `/app/web/dist` on disk. A reviewed derivative may preserve the accepted immutable backend image and replace that entire directory with exact enabled frontend bytes, keeping entrypoint, working directory, user, native libraries and backend hash unchanged. Public browser configuration comes only from the Sentry owner's approved public handoff. Derive release from the final combined source SHA, never copy a historical candidate release. No auth/upload token, source maps or private source files may enter the public artifact; private map mode stays disabled. Prove exact image/dist manifest equality and backend identity before publication/rollout.

The coordinator must report exact source/image pairs, qualification and fresh rollback identity before runtime mutation. Serialize with the Sentry owner and the shared validation lease. Recheck running verified-v1 worker/producer, config identity, pending and running work. Roll out qualified worker first, then producer; preserve verified and legacy queues and existing configuration. On failed acceptance, restore producer first, then worker using freshly verified receipt-capable immutable rollback references. Never move tasks to legacy or weaken receipt/egress verification.

Full production closure still requires permitted health/SSO/browser checks, approved actual runtime path verification and naturally occurring failure correlation between safe source and receiver fields. Current public MCP/Chat/SSO requests returned403; no access controls, credentials or settings were changed. Parent notification routing also failed. A qualified local/source candidate does not close these operational gaps.

## Local qualification on2026-10-14

Race-enabled real MCP/Redis/separate-worker integration passed all11 cases, and strict SSO TCP controls passed. Actual SDK HTTP metadata-drop control failed specifically at empty fields; restoring the hook passed. Both required `make lint` targets and combined release contracts passed. Mongo socket tests compile but skip because no disposable Mongo facility is available; they remain incomplete acceptance. Backend production sources/dependency pins were not changed by these tests.
