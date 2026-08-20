# MCP Memory Benchmark — pageindex

- **Status:** `VALID`
- **Run:** `20260820T020712.685704136Z`
- **Backend:** `local-in-process`
- **Dataset:** `mcp-memory-smoke` (`2026-08-19-v2`, SHA-256 `16ee655d76bb002fe584f31ba8236333a7a2c855457407dc535ac7cd1642268b`)
- **Plugin:** `pageindex`
- **Configuration:** top-k `5`, min-score `0.2000`, concurrency `1`, warm-up `1`, repetitions `3`, seed `42`
- **Config SHA-256:** `0515bc504460c8b7d36c10f509bcdfc92602c17ba8c273ff88b9c78e7ea60d7c`
- **Fixed reader:** disabled; answer metrics are not reported

## Quality

| Metric | Value |
|---|---:|
| Total queries | 6 |
| Evaluated queries | 6 |
| Failed queries | 0 |
| Recall@5 | 1.0000 |
| Precision@5 | 0.2400 |
| nDCG@5 | 0.6772 |
| MRR | 0.5500 |
| Hit rate@5 | 1.0000 |
| Evidence recall | 0.8000 |
| Abstention accuracy | 0.0000 |
| False-answer rate | 1.0000 |
| Error rate | 0.0000 |

## Ability slices

| Category | N | Failed | Recall@5 | nDCG@5 | MRR | Hit@5 | Evidence | Abstention | Errors |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| abstention | 1 | 0 | n/a | n/a | n/a | n/a | n/a | 0.0000 | 0.0000 |
| knowledge-update | 1 | 0 | 1.0000 | 0.4307 | 0.2500 | 1.0000 | 1.0000 | n/a | 0.0000 |
| long-document | 1 | 0 | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 0.0000 | n/a | 0.0000 |
| multi-hop | 1 | 0 | 1.0000 | 0.6934 | 0.5000 | 1.0000 | 1.0000 | n/a | 0.0000 |
| preference | 1 | 0 | 1.0000 | 0.6309 | 0.5000 | 1.0000 | 1.0000 | n/a | 0.0000 |
| single-hop | 1 | 0 | 1.0000 | 0.6309 | 0.5000 | 1.0000 | 1.0000 | n/a | 0.0000 |

## Operations

| Measurement | Mean | P50 | P95 | P99 | Max |
|---|---:|---:|---:|---:|---:|
| file_write (ms) | 0.720 | 0.667 | 0.882 | 0.912 | 0.920 |
| file_search (ms) | 0.539 | 0.465 | 0.872 | 1.233 | 1.323 |
| write→relevant (ms) | 0.537 | 0.549 | 0.591 | 0.599 | 0.600 |

- Documents per second: `1374.288`
- Mean retrieved context tokens (estimated): `82.0`

## Case results

| Query | Category | Recall@5 | nDCG@5 | MRR | Evidence | Search mean ms | Error |
|---|---|---:|---:|---:|---:|---:|---|
| `latest-value` | knowledge-update | 1.0000 | 0.4307 | 0.2500 | 1.0000 | 0.448 |  |
| `long-document-rollback` | long-document | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 0.441 |  |
| `multi-document` | multi-hop | 1.0000 | 0.6934 | 0.5000 | 1.0000 | 0.843 |  |
| `preference-drink` | preference | 1.0000 | 0.6309 | 0.5000 | 1.0000 | 0.575 |  |
| `single-hop-city` | single-hop | 1.0000 | 0.6309 | 0.5000 | 1.0000 | 0.482 |  |
| `unknown-launch-code` | abstention | 0.0000 | 0.0000 | 0.0000 | 0.0000 | 0.444 |  |

