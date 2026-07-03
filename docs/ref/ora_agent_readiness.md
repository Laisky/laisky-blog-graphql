# Ora Agent Readiness

Durable findings from public Ora sources, checked 2026-07-03.

## Current score for mcp.laisky.com

- Source: https://ora.ai/score/mcp.laisky.com and https://ora.ai/api/score/mcp.laisky.com
- Scan time: 2026-07-03T02:31:21Z; status: complete; duration: 14.759s.
- Overall: 29/100, grade D, "At Risk".
- Ora summary: MCP has OAuth 2.0 support, but lacks a public API with reachable endpoints.
- Layer totals: Discovery 4/22 on the public page; Identity 4/22 on the public page; Access 15/34 on the public page; Payments N/A; Experience 0/10. The structured API exposes a broader internal check denominator, but the page-normalized score is the user-facing result.
- Fresh stream scan after the first agent-discovery iteration completed at 2026-07-03T03:04:06Z with 63/100, grade C. Layer raw totals were Discovery 6/20, Identity 40/51, Access 92/108, Payments 1/3, Experience 0/0. The cached score API may lag the stream result.

## Reported weaknesses

- Discovery: weak developer-resource discoverability, no Wikipedia/Wikidata entity, no linked public repo with agent configs/rules, no ChatGPT app directory listing, poor semantic indexability, no third-party citation corpus, weak fresh unauthenticated description surfaces.
- Identity: no sitemap, little no-JavaScript text content, no AI crawler directives in robots.txt, no agent discovery file, no A2A agent card, no JSON-LD, no consistent description metadata, no llms.txt, no canonical markdown fallback, missing trust anchor pages, shallow/no discoverable documentation content.
- Access: no publicly reachable REST or GraphQL API surface detected, no OpenAPI/Swagger spec, no agent-friendly auth discovery metadata, no API catalog, no OAuth protected-resource metadata, no auth.md, no JSON error responses, no function-calling-compatible API spec, no NLWeb /ask endpoint, no webhook system, no WebMCP support, and no standard live MCP discovery at /.well-known/mcp.
- MCP-specific caveat: Ora found a first-party MCP server/manifest signal, OAuth endpoint, developer portal, sandbox, CLI package, SDK signal, and major crawler reachability, but repeatedly reports "No MCP server detected" for live MCP handshake/tool/resource checks because standard discovery/handshake was not reachable from the scanned homepage/domain.
- Payments and in-agent UI checks are mostly N/A because Ora classifies commerce and MCP Apps/generative UI as not applicable for this scan.

## Public Ora scoring guidance

- Source: https://ora.ai/methodology, https://ora.ai/blog/state-of-agent-readiness-2026, https://ora.ai/docs, and https://ora.ai/agents.md
- Ora says it measures the five stages of a real agent journey: Discovery, Identity, Access, Payments, and Experience.
- Methodology weights are 22, 22, 34, 12, and 10 points respectively; grades are A+ 95-100, A 86-94, B 70-85, C 48-69, D 28-47, F 0-27.
- Ora describes its scans as real-agent sessions plus protocol probes, not simple string matching. It says the framework covers 110+ checks across public URLs and standards/signals such as robots.txt, llms.txt, sitemap, JSON-LD, RFC 8288 Link headers, RFC 8414 OAuth metadata, RFC 9309 robots.txt, RFC 9727 API catalog, RFC 9728 OAuth protected-resource metadata, MCP surfaces, A2A, ACP, x402, MPP, and agentskills.io.
- Ora marks genuinely irrelevant checks as N/A so missing commerce or in-agent UI protocols do not necessarily penalize non-commerce/backend products.
- Public APIs include cached score lookup, scanning, leaderboard/discovery, feedback, badge, and check-feedback endpoints. Product feedback submission is MCP-only and uses a HATCHA agent-verification flow.
- The public score page starts a fresh scan through an event stream at `/api/scan/stream?domain=<domain>&fresh=1` and polls deep checks at `/api/deep-checks/<domain>`. Its Access probes include conventional protected API paths such as `/api`, `/api/v1`, `/v1`, `/v2`, and `/agent/auth`, looking for JSON errors and `WWW-Authenticate: Bearer resource_metadata="..."` auth discovery.
