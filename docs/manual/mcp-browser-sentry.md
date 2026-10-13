# MCP browser error reporting

Task 20261012-08 is browser errors only, on the exact `mcp.laisky.com` hostname. Standalone SSO, CV, chat2 and other products remain disabled. This does not instrument the Go server, MCP protocol, traces, replay, HTTP payloads or tools. The MCP Sentry project does not currently exist; creating it and approving project configuration is a separate deployment prerequisite. Never reuse the blog project's DSN.

## Deployment configuration

Vite build-time values must all be supplied explicitly by the approved deployment:

| Variable | Required value |
| --- | --- |
| `VITE_SENTRY_ENABLED` | `true` |
| `VITE_SENTRY_SITE` | `mcp` |
| `VITE_SENTRY_DSN` | Approved MCP project public browser DSN; HTTPS Sentry ingest host, 32-character hex public key, numeric project ID; no private key/password/query/fragment |
| `VITE_SENTRY_ENVIRONMENT` | `development`, `staging` or `production` |
| `VITE_SENTRY_RELEASE` | Exact deployed source Git SHA (7–40 lowercase hex characters); reported as `mcp@<sha>` |

Values are build-time variables, not container-runtime environment overrides of prebuilt JavaScript. The Docker frontend build accepts these five public values as build arguments. Existing CI release controls are unchanged and do not supply these values. The deployment owner must separately approve and supply the matching Docker build arguments; adding environment variables only to the running container will not enable reporting. Never put an upload auth token in any `VITE_*` variable, source, image, environment example, or browser bundle. The public DSN is routing configuration and is intentionally browser-visible; no secret/token retrieval is required for this PR.

Missing/invalid configuration or an unsupported hostname skips loading the SDK. Duplicate initialization is idempotent. SDK loading is bounded to three seconds; timed-out imports cannot install a late client. React root defaults remain intact when reporting is disabled or fails. SDK import/init/capture and transport failures preserve UI startup; outgoing collector requests omit credentials and referrers; diagnostics are static text and contain no exception payload or configuration. Partially installed SDK hooks cannot send errors after failed initialization.

## Privacy and diagnosis tradeoffs

Both beforeSend and a final errors-only envelope transport construct a bounded allowlist. The transport also covers SDK-internal errors that bypass beforeSend, strips envelope trace/header metadata and attachments, and drops non-error envelope types. The allowlist includes: native error types, generic error messages, fixed site/environment/release identifiers, recognized source labels, and at most four exceptions with twenty asset frames each. Stack filenames accept only the exact loaded entry module URL captured from `import.meta.url`, with the same-origin Vite `index-<8-char hash>.js` or `browser-sentry-<8-char hash>.js` shape; query strings/fragments are discarded. SDK and other unlisted chunks are omitted even if their names have a valid hash shape, preventing fabricated sensitive hashes from being forwarded. Frame function names, variables and source context are omitted. Arbitrary asset names and route URLs are omitted because they may contain user content. If bundling changes names or additional chunk symbolication is needed, extend the trusted actual-asset inventory with privacy regression coverage instead of accepting arbitrary filenames or hashes.

Requests/responses, headers/cookies, API keys/tokens, auth and local storage, prompts/completions/history, MCP arguments/results, user metadata, breadcrumbs, contexts, extra fields, fingerprints, envelope attachments and arbitrary exception messages are excluded. Strict projection intentionally sacrifices message detail and some frames. Router fallback effects report once under StrictMode. Primitive React-caught/router pairs use a 32-entry, 500ms local two-word hash cache (no raw prompt cache or emitted fingerprint); same-source and distinct errors remain reportable. Narrow-window hash collision risk can suppress a distinct paired primitive error. Primitive strings above 64KiB are dropped from manual capture to bound work and retention. Stack positions, approved asset names and release correlation remain available. No real telemetry event has been sent. Synthetic tests use a memory-only transport; they are local evidence, not project ingestion/CSP/runtime proof.

## Gated rollout and private source maps

1. Obtain explicit authorization for the MCP project, approved public DSN, retention/access policy and Sentry-side IP storage restriction. Browser requests necessarily expose connection IP to the collector; SDK user fields are stripped but collector policy requires separate approval. Confirm CSP permits only that approved ingest destination.
2. Separately approve build-variable wiring and release publication/deployment. Ordinary default-branch changes can publish/deploy this repository; this Draft PR does not authorize merging, CI control changes, image publication or rollout.
3. For a future approved private symbolication build, enable hidden source maps only in an isolated upload stage. Use the approved MCP project and identical `mcp@<sha>` release; upload matching generated JS/map filenames with the appropriate URL prefix through classic release-based correlation. Debug ID correlation would require a separately reviewed privacy allowlist extension; this sender drops debug metadata. Keep upload tokens in the private upload stage only, with narrow grants. Source files can contain sensitive material: review access and map content before upload.
4. After verified private upload, remove/exclude every `*.map` from the public deployment artifact and image. Hidden maps are still publicly readable if shipped. Deny map requests at the asset server as defense in depth, scan the final image/artifact, and verify map URLs return 404/403. Do not add an upload plugin/token or generate maps by default in this PR.
5. With project/configuration approval, validate one benign synthetic event on an approved environment and exact target host, confirm sanitization and release/stack symbolication in Sentry, and verify SSO/other hosts emit nothing. Check MCP tool and sign-in journeys plus disabled-config rollback. This final real event/runtime check remains gated.

## Rollback and local acceptance

Remove `VITE_SENTRY_ENABLED` or the DSN and rebuild/redeploy through the existing approved release process; SDK loading stops. Revert the feature commit if needed. Neither rollback nor PR creation authorizes a production deployment.

Run `pnpm run lint`, `pnpm run test`, and `pnpm run build` in `web/` using the Docker-matched pnpm 10.19.0. Regression coverage includes exact host/config gating, privacy/size/URL projection, mutable-config isolation, duplicate init/capture, SDK import/init/capture failure, real SDK memory transport global errors/rejections/internal-event bypass, mounted React 19 boundary/uncaught rendering, and router fallback. Record exact commit/results in the Draft PR. No Go code changes require backend acceptance.
