# MCP Credential Policy

Last updated: 2026-11-16 (UTC)

This document records a product-security decision. It is kept apart from the authenticator's design (`docs/arch/mcp_auth.md`) and from its rate-limit tuning (`docs/manual/mcp.md`, Credential Validation), because it decides *who may use the platform*, not *how* validation is implemented.

## 1. Decision

**A caller is a tenant exactly while one-api's `TokenAuth` accepts its API key.**

- The tenant is the SHA-256 hash of the key. File, memory, user-request, ask_user and call-log data are owned by that hash. The hash scheme never changes, so existing data keeps its owner.
- one-api is the only authority. This server keeps no account, password or token store of its own for MCP callers.
- A key that one-api does not accept gets no tenant on any entrypoint: the MCP transport, the `/tools/*/api` consoles, the GraphQL fields shared with MCP tools, ArweaveUpload's API-key path, and Telegram's key linking all ask the same authenticator.

## 2. Consequences callers can see

| Key state at one-api | Paid tools | Free tools (files, memory, user requests, ask_user) | Stored data |
| --- | --- | --- | --- |
| Active, with quota | Allowed | Allowed | Readable and writable |
| Quota exhausted | Refused | **Refused** (`401`) | Kept, unreachable until topped up |
| Revoked, expired, disabled | Refused | **Refused** | Kept, unreachable with this key |
| Subnet-restricted to the end user's network | Refused | **Refused** (one-api compares the subnet with this server's egress address) | Kept, unreachable |
| one-api unavailable | A key accepted within `positive_ttl + stale_grace` keeps working; others get `503` | Same | Unchanged |

Before PR #95, any non-empty bearer became a tenant, so exhausted, revoked or unknown keys could still use free tools and reach stored data. That was the vulnerability (#86); the rows marked **Refused** are the intended behavior, not a regression.

Callers that relied on a depleted key to read their files or request history must top the key up, or move their data with an active key. A new key is a new tenant: data is never migrated between key hashes automatically.

## 3. Why

- **No second identity system.** one-api already issues, revokes and restricts keys. A local allowlist would drift from it and need its own revocation path.
- **Fail closed.** "Non-empty" is not proof of anything; one-api's acceptance is.
- **Exhaustion is not distinguishable.** one-api answers `401` for unknown, revoked, expired, disabled and quota-exhausted tokens alike, and offers no route that validates a token while skipping the quota check (`docs/ref/20261108_mcp_ingress_auth_body_budget.md`). Accepting exhausted keys would therefore mean accepting every `401`.

## 4. Accepted residual risks

- **Revocation lag.** A revoked key keeps working for up to `positive_ttl` (default 5 minutes), or up to `positive_ttl + stale_grace` (default 35 minutes) while one-api itself gives no verdict.
- **one-api token-store failures look like rejections.** one-api answers its own token-store failures with `401`. During such a failure, keys whose positive cache expired are rejected. `mcp_auth_known_key_rejections_total` makes this visible.
- **Telegram answers are delegated.** Answering an ask_user question from Telegram is authorized by the Telegram account ↔ key-hash binding, which was proven by a validated key when it was made. The answer is only readable by a caller that authenticates with that key again.

## 5. Revisit when

- free-tier storage access is wanted (data reachable without quota);
- another billing provider or authority is added;
- account-level identities independent of API keys are introduced (several keys, one tenant);
- one-api gains a quota-agnostic validation route.

Any of these changes the table in section 2 and needs an explicit owner decision recorded here.
