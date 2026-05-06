# RAGAS prompt provenance

Phase 1 placeholder. The prompt strings in `../../ragas.go` are clearly marked
`// TODO(eval): port from ragas v0.4.x source` and MUST be replaced before any
absolute RAGAS score is used to decide a §7.5 hard gate.

| Metric                    | Source path in ragas==0.4.x       | Status                                     |
| ------------------------- | --------------------------------- | ------------------------------------------ |
| `faithfulness`            | `ragas/metrics/_faithfulness.py`  | TBD — port from ragas==0.4.x at `<pinned>` |
| `context_precision`       | `ragas/metrics/_context_precision.py` | TBD — port from ragas==0.4.x at `<pinned>` |
| `context_recall`          | `ragas/metrics/_context_recall.py` | TBD — port from ragas==0.4.x at `<pinned>` |
| `context_entities_recall` | `ragas/metrics/_context_entities_recall.py` | TBD — port from ragas==0.4.x at `<pinned>` |
| `answer_relevancy`        | `ragas/metrics/_answer_relevance.py` | TBD — port from ragas==0.4.x at `<pinned>` |
| `answer_correctness`      | `ragas/metrics/_answer_correctness.py` | TBD — port from ragas==0.4.x at `<pinned>` |

Once the ports land, this file enumerates each prompt's exact upstream commit
hash and the sha256 of the captured golden copy in this directory. A unit test
in `ragas_test.go` then asserts that the constants in `ragas.go` byte-match the
captured goldens, so prompt drift fails CI rather than silently shifting the
metric.
