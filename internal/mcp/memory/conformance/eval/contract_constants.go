package eval

// Shared wire-format and storage identifiers keep these contracts consistent.
const (
	evalAnswerCorrectness     = "answer_correctness"
	evalAnswerRelevancy       = "answer_relevancy"
	evalContextEntitiesRecall = "context_entities_recall"
	evalContextPrecision      = "context_precision"
	evalContextRecall         = "context_recall"
	evalEvalHarness           = "eval-harness"
	evalFaithfulness          = "faithfulness"
	evalFinancebench150       = "financebench-150"
	metricHitAt5              = "hit@5"
	evalMissing               = "missing"
	evalMrr                   = "mrr"
	metricUnavailable         = "n/a"
	metricNDCGAt10            = "ndcg@10"
	evalOpsProbe              = "ops-probe"
	evalRagas                 = "ragas"
	metricRecallAt10          = "recall@10"
	evalRedteam               = "redteam"
	evalSkipped               = "skipped"
	evalType                  = "type"
)
