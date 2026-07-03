# MCP Agent Readiness Optimization Manual

This manual is a reusable playbook for improving the agent-friendliness of an MCP-backed public site and for validating those improvements with Ora or similar agent-readiness scanners.

The goal is not to game a scanner. The goal is to make a real agent able to discover the service, understand what it does, authenticate safely, call the correct endpoint, recover from errors, and cite the service with confidence.

## Menu

- [MCP Agent Readiness Optimization Manual](#mcp-agent-readiness-optimization-manual)
  - [Menu](#menu)
  - [1. Operating Loop](#1-operating-loop)
  - [2. Evidence Rules](#2-evidence-rules)
  - [3. Optimization Checklist](#3-optimization-checklist)
    - [3.1 Discovery and Crawlability](#31-discovery-and-crawlability)
    - [3.2 Identity and Entity Signals](#32-identity-and-entity-signals)
    - [3.3 MCP Transport and Discovery](#33-mcp-transport-and-discovery)
    - [3.4 Authentication Discovery](#34-authentication-discovery)
    - [3.5 API Catalog and OpenAPI](#35-api-catalog-and-openapi)
    - [3.6 JSON Errors and Probe Endpoints](#36-json-errors-and-probe-endpoints)
    - [3.7 WebMCP](#37-webmcp)
    - [3.8 Web Bot Auth and HTTP Message Signatures](#38-web-bot-auth-and-http-message-signatures)
    - [3.9 NLWeb, Webhooks, and Multi-Surface Access](#39-nlweb-webhooks-and-multi-surface-access)
    - [3.10 Off-Site Discovery](#310-off-site-discovery)
  - [4. Acceptance Matrix](#4-acceptance-matrix)
  - [5. Ora Evaluation Commands](#5-ora-evaluation-commands)
  - [6. Publishing Workflow](#6-publishing-workflow)
  - [7. Common Failure Patterns](#7-common-failure-patterns)
  - [8. References](#8-references)

## 1. Operating Loop

Use this loop for every score-improvement pass:

1. Capture a fresh baseline from the score provider.
2. Extract failed and warning checks, grouped by layer.
3. Classify each finding as one of:
   - `site-fixable`: content, metadata, HTTP headers, static files, or route behavior controlled by this repo.
   - `product-work`: a real feature such as webhooks, NLWeb, resources, prompts, or SDK packaging.
   - `external`: registry listing, ChatGPT app listing, Wikipedia/Wikidata, third-party citations, search index presence.
   - `not-applicable`: commerce, in-agent UI, or protocol surfaces that do not match the product.
4. Implement only truthful changes. Do not advertise a protocol, payment method, SDK, or tool surface that does not exist.
5. Verify locally with focused tests and static checks.
6. Publish with the repo's deployment workflow.
7. Poll the live domain until the changed page, header, or endpoint is actually served.
8. Run a fresh scan and record the new score, timestamp, layer totals, and remaining failures.

Treat the live domain as authoritative. A committed change is not accepted until production serves it.

## 2. Evidence Rules

Use direct evidence for each claim:

- Content exists: `curl` or browser output from the public URL.
- Content type is correct: response headers from the public URL.
- MCP transport works: JSON-RPC `initialize` succeeds on the public endpoint.
- Authentication metadata works: unauthenticated API probes return `401`, JSON body, and a `WWW-Authenticate` header with `resource_metadata`.
- API catalog works: `/.well-known/api-catalog` returns an RFC 9727 linkset document.
- OpenAPI works: `openapi.json` parses, has operation IDs, request/response schemas, and linked auth semantics.
- Score changed: fresh Ora stream scan, not only a cached score page.
- External discovery works: independent search, registry page, or directory listing is visible without being logged in.

Avoid using source files as final proof for public-readiness checks. Source files prove intent; live HTTP responses prove deployment.

## 3. Optimization Checklist

### 3.1 Discovery and Crawlability

Agents need a low-noise, no-JavaScript path before they can use a rich app.

Implement:

- A static root fallback with clear product name, canonical endpoint, core use cases, and machine-readable links.
- `llms.txt` and, when useful, `llms-full.txt`.
- `agents.md` or equivalent agent instructions.
- `robots.txt` that does not block major crawlers or agent crawlers unless intentionally restricted.
- `sitemap.xml` with canonical documentation and well-known resources.
- Markdown fallbacks for important HTML/JSON resources where agent context benefits from concise text.
- Link headers from the homepage for sitemap, markdown alternate, OpenAPI, MCP discovery, and auth docs.

Accept when:

- `curl -sS https://mcp.laisky.com/` contains the service name, endpoint, major use cases, and links to agent resources.
- `curl -sSI https://mcp.laisky.com/` shows relevant `Link` headers.
- `curl -sS https://mcp.laisky.com/llms.txt` returns concise agent navigation.
- `curl -sS https://mcp.laisky.com/sitemap.xml` includes the canonical agent resources.

### 3.2 Identity and Entity Signals

Agents and citation systems need consistent entity names and authority links.

Implement:

- Consistent product and organization names across title, meta description, Open Graph, JSON-LD, OpenAPI, and Markdown docs.
- JSON-LD for `SoftwareApplication`, `Organization`, `Service`, `FAQPage`, and `BreadcrumbList` when applicable.
- `sameAs` links to authoritative profiles such as GitHub, project homepage, package pages, registry pages, Wikidata, Wikipedia, or LinkedIn.
- A stable icon/logo URL and canonical URL.
- A source repository link when public.

Accept when:

- Homepage JSON-LD parses as valid JSON.
- `sameAs` contains real, public, authoritative URLs.
- Ora identity checks no longer report missing no-JavaScript text, missing JSON-LD, or inconsistent entity names.

### 3.3 MCP Transport and Discovery

Remote MCP should be discoverable and callable from the advertised endpoint.

Implement:

- A canonical MCP Streamable HTTP endpoint.
- A public discovery document at `/.well-known/mcp` for `GET` and `HEAD`.
- A public server card at `/.well-known/mcp/server-card.json` or an equivalent linked manifest.
- `rel="mcp"` links in HTML and/or HTTP `Link` headers.
- Non-GET requests to a discovery alias should either be routed to the real MCP transport or return a clear JSON error. Do not let scanners hit an alias that 404s while the root transport works.
- Tool descriptions and input schemas should be concise and specific. Every tool needs a stable name, description, and typed arguments.
- If resources or prompts are supported, advertise them in the MCP initialize capabilities and implement `resources/list` or `prompts/list`.

Accept when:

```bash
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}' \
  | curl -sS -D /tmp/mcp.headers \
      -H 'Content-Type: application/json' \
      -H 'Accept: application/json, text/event-stream' \
      --data-binary @- \
      https://mcp.laisky.com/
```

Pass criteria:

- Status is `200`.
- Body contains `protocolVersion`, `capabilities`, and `serverInfo`.
- If `/.well-known/mcp` is advertised as callable, the same initialize probe must work there or the advertised document must clearly point clients to the callable endpoint.

### 3.4 Authentication Discovery

Agents should be able to discover how to authenticate without reading a human-only page.

Implement:

- `auth.md` with token acquisition, token placement, examples, and secret-handling rules.
- OAuth 2.0 Protected Resource Metadata at `/.well-known/oauth-protected-resource`.
- OAuth authorization server metadata where applicable.
- `WWW-Authenticate: Bearer resource_metadata="..."` on protected unauthenticated API probes.
- JSON responses for unauthenticated requests.
- Clear distinction between public metadata endpoints and protected tool-call endpoints.

Accept when:

```bash
curl -sS -D /tmp/api.headers -o /tmp/api.body https://mcp.laisky.com/api
grep -i '^www-authenticate:' /tmp/api.headers
jq . /tmp/api.body
curl -sS https://mcp.laisky.com/.well-known/oauth-protected-resource | jq .
```

Pass criteria:

- `/api` or another advertised probe returns `401`.
- Body is JSON.
- `WWW-Authenticate` includes the protected-resource metadata URL.
- Metadata URLs are public and parse as JSON.

### 3.5 API Catalog and OpenAPI

Even MCP-first products benefit from a conventional API discovery surface.

Implement:

- `/.well-known/api-catalog` using RFC 9727 linkset JSON.
- `openapi.json` linked from the homepage, catalog, MCP discovery, and docs.
- Operation IDs suitable for function calling.
- Typed request bodies and response schemas.
- Security schemes and examples.
- Markdown summary for the OpenAPI file if the raw schema is large.

Accept when:

```bash
curl -sS -D /tmp/catalog.headers -o /tmp/catalog.json https://mcp.laisky.com/.well-known/api-catalog
grep -i 'application/linkset+json' /tmp/catalog.headers
jq . /tmp/catalog.json
jq '.paths | keys' web/public/openapi.json
```

Pass criteria:

- Catalog parses as JSON and links to the public API specs.
- OpenAPI parses as JSON.
- Every callable operation has an `operationId`.
- Important inputs and outputs are typed, not just free-form `object`.

### 3.6 JSON Errors and Probe Endpoints

Scanners commonly probe conventional API paths. A pure browser app returning HTML or `404` on those paths looks less agent-ready.

Implement:

- Stable probe routes such as `/api`, `/api/v1`, `/v1`, `/v2`, or `/agent/auth` when they are linked or expected by the scanner.
- JSON error bodies for unauthenticated or unsupported calls.
- `Allow` headers for supported methods.
- `HEAD` and `OPTIONS` behavior where useful.
- Do not call an endpoint "docs" if it is only an auth probe.

Accept when:

```bash
for path in /api /api/v1 /v1 /v2 /agent/auth; do
  curl -sS -D "/tmp${path//\//_}.headers" -o "/tmp${path//\//_}.json" "https://mcp.laisky.com${path}"
  jq . "/tmp${path//\//_}.json"
done
```

Pass criteria:

- Protected probes return JSON, not HTML.
- Unknown protected access is `401` or a documented client error, not an opaque `404`.
- Error objects contain stable fields such as `error`, `error_description`, and metadata links.

### 3.7 WebMCP

WebMCP is still experimental. Treat it as a browser-facing enhancement, not a replacement for server-side MCP.

Implement only if truthful:

- `/.well-known/webmcp` manifest.
- `rel="webmcp"` in the homepage.
- Visible homepage link to the manifest.
- If the app actually exposes browser tools, register them in the client-side WebMCP mechanism and document their scope.

Accept when:

```bash
curl -sS -D /tmp/webmcp.headers -o /tmp/webmcp.json https://mcp.laisky.com/.well-known/webmcp
grep -i 'application/json' /tmp/webmcp.headers
jq . /tmp/webmcp.json
curl -sS https://mcp.laisky.com/ | grep -i webmcp
```

Pass criteria:

- Manifest is public JSON.
- Homepage advertises it in both HTML metadata and visible/no-JavaScript content.
- The manifest points to the actual callable MCP endpoint or real browser tools.

### 3.8 Web Bot Auth and HTTP Message Signatures

HTTP Message Signatures are standardized by RFC 9421. Web Bot Auth-style key directories are useful only when a service actually signs bot requests or verifies signed inbound requests.

Implement:

- If signing/verifying is implemented, publish `/.well-known/http-message-signatures-directory` with a `keys` array containing public JWK entries.
- Include `kid`, key type, curve/algorithm, public key material, and validity metadata.
- Rotate keys deliberately and preserve old keys long enough for clients to refresh.
- If signing is not implemented, avoid implying enforcement. A status document may still say signatures are not required, but it should not be treated as a completed Web Bot Auth implementation.

Accept when:

```bash
curl -sS https://mcp.laisky.com/.well-known/http-message-signatures-directory | jq '.keys'
```

Pass criteria:

- For a real implementation, `.keys` is a non-empty array of valid public keys.
- For a non-implementation, documentation is explicit that bearer tokens are the current auth mechanism.

### 3.9 NLWeb, Webhooks, and Multi-Surface Access

These are product features, not metadata fixes.

Consider implementing:

- NLWeb `/ask` endpoint for natural-language questions over public site content.
- Streaming variant for long-running answers.
- Webhook registration, event catalog, delivery logs, retry policy, and signing.
- Additional MCP surfaces such as resources, prompts, or MCP Apps when they match real product needs.

Accept when:

- `/ask` accepts a documented JSON request and returns grounded JSON answers.
- Streaming responses use a documented content type and event format.
- Webhooks have real create/list/delete/test endpoints and signed delivery.
- MCP `initialize` advertises only capabilities that are actually implemented.

### 3.10 Off-Site Discovery

Some readiness points cannot be fixed from the application repo alone.

Optimize:

- Public GitHub README and repository topics.
- Package pages for SDKs or CLI clients.
- Official MCP Registry listing.
- ChatGPT Apps Directory submission if the service has an in-agent app surface.
- Wikidata/Wikipedia only when notability requirements are genuinely satisfied.
- Third-party technical posts, examples, or documentation pages that cite the canonical domain.
- Search snippets for the exact brand plus category, for example `Laisky MCP developer tools`.

Accept when:

- Search engines return relevant pages for brand and use-case queries.
- The MCP registry entry is public and links to the canonical domain.
- Directory submissions are approved or explicitly tracked as pending external work.
- Ora discovery checks move without adding misleading claims to the product page.

## 4. Acceptance Matrix

| Area | Implementation evidence | Live acceptance | Score checks usually affected |
| --- | --- | --- | --- |
| Static agent docs | `llms.txt`, `agents.md`, Markdown homepage | `curl` public files and inspect no-JS homepage text | Identity, discovery |
| Sitemap and robots | `sitemap.xml`, `robots.txt` | Public URLs parse and include agent resources | Identity, crawl depth |
| JSON-LD | Homepage structured data | JSON parses and names match product | Identity, citation quality |
| MCP discovery | `/.well-known/mcp`, server card, `rel=mcp` | GET manifest and initialize probe work as advertised | Access, MCP detection |
| Auth discovery | `auth.md`, OAuth metadata, `WWW-Authenticate` | Protected probe returns JSON `401` with metadata | Access, onboarding |
| API catalog | RFC 9727 linkset | Public catalog has `application/linkset+json` and links specs | Access, API discovery |
| OpenAPI | `openapi.json` | JSON parses, operation IDs and schemas present | Access, function calling |
| JSON errors | Probe routes and API handlers | Conventional paths return structured JSON errors | Access, error recovery |
| WebMCP | Manifest and homepage link | Public manifest and homepage mention | Access, browser agents |
| Web Bot Auth | Real key directory | Non-empty valid `keys` array when implemented | Access, bot auth |
| NLWeb | `/ask` endpoint | JSON and streaming answers work | Access, NLWeb checks |
| Webhooks | Event and delivery system | Register, list, receive, verify signatures | Access, webhook checks |
| External citations | Registry, directories, public articles | Independent public URLs and search results | Discovery |

## 5. Ora Evaluation Commands

Use fresh stream scans for iteration. Cached score endpoints can lag.

```bash
scan=/tmp/ora-scan-stream.txt
timeout 180 curl -N -L --silent --show-error \
  'https://ora.ai/api/scan/stream?domain=mcp.laisky.com&fresh=1' \
  -o "$scan"

perl -ne 'if(/^data: (\{.*"type":"scan_complete".*\})$/){print $1}' "$scan" \
  | jq '.result' > /tmp/ora-scan-result.json

jq '. | {score,grade,scannedAt,layers: [.layers[] | {id,score,maxScore}]}' \
  /tmp/ora-scan-result.json

jq -r '.layers[] | "\(.id) \(.score)/\(.maxScore)", (.checks[] | select(.status != "pass" and .maxScore > 0) | "  \(.id) [\(.status)] \(.score)/\(.maxScore): \(.details|tostring|gsub("\n";" "))")' \
  /tmp/ora-scan-result.json
```

Record the score, grade, scan timestamp, layer totals, and residual failures in `docs/ref/ora_agent_readiness.md` when the findings are durable.

## 6. Publishing Workflow

1. Keep changes focused on one readiness class per commit when possible.
2. Run required tests:
   - Go changes: `make lint` and `go test -race -cover ./...`.
   - Frontend changes: `pnpm run lint`, `pnpm run test`, and `pnpm run build` in `web/`.
3. Restore unrelated generated-file or package-manager churn before committing.
4. Commit with a message that names the readiness surface changed.
5. Publish with `git ps`.
6. Poll production with no-cache headers until the changed URL or header is live.
7. Run a fresh Ora scan.
8. Repeat until the target score is reached or the remaining work is clearly external/product work.

## 7. Common Failure Patterns

- **Committed but not live:** scanners only see production. Poll the endpoint before rescanning.
- **HTML where JSON is expected:** API probes and metadata endpoints need JSON content types and JSON bodies.
- **Thin docs link:** do not label an auth probe as API documentation. Link to `openapi.json`, `auth.md`, or a real docs page instead.
- **Advertised alias 404s:** if a homepage links to `/.well-known/mcp`, make sure the exact path works for the methods scanners use or clearly points to the real endpoint.
- **Empty protocol shells:** publishing a manifest for a protocol without real support may earn no points and can mislead agents.
- **Over-broad claims:** do not advertise OAuth, Web Bot Auth, WebMCP tools, webhooks, SDKs, or registry listings before they exist.
- **Search-index lag:** off-site citations and search results may take days or weeks to affect discovery scores.
- **Scanner N/A confusion:** N/A checks are not necessarily problems. Do not add commerce or UI protocols just because they appear in a report.

## 8. References

Checked on 2026-07-03:

- MCP specification: https://modelcontextprotocol.io/specification/2025-11-25
- MCP Streamable HTTP transport: https://modelcontextprotocol.io/specification/2025-11-25/basic/transports
- MCP tools: https://modelcontextprotocol.io/specification/2025-06-18/server/tools
- MCP resources: https://modelcontextprotocol.io/specification/2025-11-25/server/resources
- llms.txt proposal: https://llmstxt.org/
- Chrome Lighthouse `llms.txt` guidance: https://developer.chrome.com/docs/lighthouse/agentic-browsing/llms-txt
- RFC 9727 API Catalog: https://datatracker.ietf.org/doc/rfc9727/
- RFC 9728 OAuth 2.0 Protected Resource Metadata: https://datatracker.ietf.org/doc/rfc9728/
- RFC 9421 HTTP Message Signatures: https://www.rfc-editor.org/info/rfc9421/
- Web Bot Auth draft architecture: https://datatracker.ietf.org/doc/draft-meunier-web-bot-auth-architecture/
- Official MCP Registry: https://registry.modelcontextprotocol.io/
- OpenAI Apps SDK submission guidance: https://developers.openai.com/apps-sdk/deploy/submission
- NLWeb introduction: https://news.microsoft.com/source/features/company-news/introducing-nlweb-bringing-conversational-interfaces-directly-to-the-web/
- Project findings: `docs/ref/ora_agent_readiness.md`
