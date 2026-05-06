PLACEHOLDER — datasets not yet provisioned. Every cell below is `n/a` until the
§7.3 golden datasets (`memory-bench-internal-v1`, `memory-bench-ragas-v1`,
`financebench-150`, `longmemeval_s`, `beam-1m-200`) land via git-LFS and the
`cmd/eval-plugin` driver gains a `--plugin=pageindex` construction path. The
file follows the proposal §7.4 template byte-for-byte so a future regenerated
scorecard drops in cleanly.

plugin: pageindex                     run_id: pending:datasets
golden_set:        memory-bench-internal-v1
ragas_set:         memory-bench-ragas-v1
public_set_a:      financebench-150
public_set_b:      longmemeval_s
public_set_c:      beam-1m-200

[Retrieval quality — internal]
recall@10              n/a
ndcg@10                n/a
mrr                    n/a
hit@5                  n/a
ndcg@10 (long-doc)     n/a

[Generation quality — RAGAS v0.4]
faithfulness           n/a
context_recall         n/a
context_precision      n/a
answer_correctness     n/a
answer_relevancy       n/a
context_entities_recall n/a

[Public benchmarks]
financebench-150                  n/a
longmemeval_s (overall + 7 cats)  n/a
beam-1m (200, 6 cats)             n/a

[Operational]
file_search p50/p95/p99 (ms)         n/a
file_write→searchable p95 (ms)        n/a
tokens_in/out per search (mean,p95)   n/a
$ per 1K searches @ <model>           n/a
index throughput (pages/min/worker)   n/a
cold-p95 - warm-p95 (ms)              n/a

[Adversarial]
prompt_injection blocked       n/a
cross_tenant_hits              n/a
supersession_correct           n/a
gdpr_delete_recall_ms_p95      n/a
weekly_drift_ndcg10            n/a
