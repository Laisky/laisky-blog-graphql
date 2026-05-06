plugin: rag                           run_id: e6caab4:2026-05-06T04:02:46Z
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
prompt_injection blocked       12/12
cross_tenant_hits              0
supersession_correct           n/a
gdpr_delete_recall_ms_p95      0
weekly_drift_ndcg10            0.00pp
