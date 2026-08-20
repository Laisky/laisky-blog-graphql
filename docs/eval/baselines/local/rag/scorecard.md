# MCP Memory Benchmark — rag

- **Status:** `VALID`
- **Run:** `20260820T020712.411497534Z`
- **Backend:** `local-in-process`
- **Dataset:** `mcp-memory-smoke` (`2026-08-19-v2`, SHA-256 `16ee655d76bb002fe584f31ba8236333a7a2c855457407dc535ac7cd1642268b`)
- **Plugin:** `rag`
- **Configuration:** top-k `5`, min-score `0.2000`, concurrency `1`, warm-up `1`, repetitions `3`, seed `42`
- **Config SHA-256:** `721af7f9f31d677507af76a086248a74e7bea353392805aa8b6afe98952cbc3a`
- **Fixed reader:** disabled; answer metrics are not reported

## Quality

| Metric | Value |
|---|---:|
| Total queries | 6 |
| Evaluated queries | 6 |
| Failed queries | 0 |
| Recall@5 | 1.0000 |
| Precision@5 | 0.2400 |
| nDCG@5 | 0.9839 |
| MRR | 1.0000 |
| Hit rate@5 | 1.0000 |
| Evidence recall | 1.0000 |
| Abstention accuracy | 0.0000 |
| False-answer rate | 1.0000 |
| Error rate | 0.0000 |

## Ability slices

| Category | N | Failed | Recall@5 | nDCG@5 | MRR | Hit@5 | Evidence | Abstention | Errors |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| abstention | 1 | 0 | n/a | n/a | n/a | n/a | n/a | 0.0000 | 0.0000 |
| knowledge-update | 1 | 0 | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 1.0000 | n/a | 0.0000 |
| long-document | 1 | 0 | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 1.0000 | n/a | 0.0000 |
| multi-hop | 1 | 0 | 1.0000 | 0.9197 | 1.0000 | 1.0000 | 1.0000 | n/a | 0.0000 |
| preference | 1 | 0 | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 1.0000 | n/a | 0.0000 |
| single-hop | 1 | 0 | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 1.0000 | n/a | 0.0000 |

## Operations

| Measurement | Mean | P50 | P95 | P99 | Max |
|---|---:|---:|---:|---:|---:|
| file_write (ms) | 0.160 | 0.137 | 0.245 | 0.259 | 0.263 |
| file_search (ms) | 0.087 | 0.084 | 0.109 | 0.124 | 0.128 |
| write→relevant (ms) | 0.113 | 0.117 | 0.124 | 0.126 | 0.126 |

- Documents per second: `6126.860`
- Mean retrieved context tokens (estimated): `104.5`

## Case results

| Query | Category | Recall@5 | nDCG@5 | MRR | Evidence | Search mean ms | Error |
|---|---|---:|---:|---:|---:|---:|---|
| `latest-value` | knowledge-update | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 0.085 |  |
| `long-document-rollback` | long-document | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 0.073 |  |
| `multi-document` | multi-hop | 1.0000 | 0.9197 | 1.0000 | 1.0000 | 0.102 |  |
| `preference-drink` | preference | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 0.077 |  |
| `single-hop-city` | single-hop | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 0.087 |  |
| `unknown-launch-code` | abstention | 0.0000 | 0.0000 | 0.0000 | 0.0000 | 0.095 |  |

