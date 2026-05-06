// Package eval provides the pure-Go RAGAS v0.4 metric reimplementations used
// by the plugin scorecard. The prompt strings below are placeholders. The real
// templates must be ported byte-for-byte from the upstream `ragas==0.4.x`
// source at the pinned commit recorded in
// testdata/ragas_prompts/PROVENANCE.md, which is also where the source paths
// for each metric prompt live. Until that port lands, every metric returns the
// scripted output of the supplied LLMJudge so end-to-end orchestration is
// testable; absolute scores produced against the placeholder prompts must NOT
// be used for the §7.5 gating thresholds.
package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"os"

	errors "github.com/Laisky/errors/v2"
)

// JudgeRequest is the payload sent to a judge LLM through the Responses API.
type JudgeRequest struct {
	Model        string
	Prompt       string
	Schema       map[string]any
	MaxOutTokens int
	Temperature  float32
}

// JudgeResponse is the parsed structured-output reply from the judge.
type JudgeResponse struct {
	Output       map[string]any
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	JudgeID      string
}

// LLMJudge is the abstraction over the production Responses-API LLM client.
type LLMJudge interface {
	Judge(ctx context.Context, req JudgeRequest) (JudgeResponse, error)
}

// EmbeddingClient drives the cosine-based answer_relevancy metric.
type EmbeddingClient interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// RAGASSample is one tuple from memory-bench-ragas-v1.
type RAGASSample struct {
	ID                string   `json:"id"`
	Question          string   `json:"question"`
	Answer            string   `json:"answer"`
	Contexts          []string `json:"contexts"`
	ReferenceAnswer   string   `json:"reference_answer"`
	ReferenceContexts []string `json:"reference_contexts"`
}

// RAGASOpts controls the per-metric judge invocation.
type RAGASOpts struct {
	Model        string
	MaxOutTokens int
	Temperature  float32
}

// RAGASMetricStats reports mean and p95 over a sample.
type RAGASMetricStats struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	P95    float64 `json:"p95"`
	Status string  `json:"status,omitempty"` // "ok" | "skipped"
}

// RAGASReport is the per-metric aggregate over a sample set.
type RAGASReport struct {
	Faithfulness          RAGASMetricStats `json:"faithfulness"`
	ContextPrecision      RAGASMetricStats `json:"context_precision"`
	ContextRecall         RAGASMetricStats `json:"context_recall"`
	ContextEntitiesRecall RAGASMetricStats `json:"context_entities_recall"`
	AnswerRelevancy       RAGASMetricStats `json:"answer_relevancy"`
	AnswerCorrectness     RAGASMetricStats `json:"answer_correctness"`
}

// LoadRAGASSamples parses *.jsonl tuples for the RAGAS suite.
func LoadRAGASSamples(path string) ([]RAGASSample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.Wrapf(err, "open ragas golden %s", path)
	}
	defer f.Close()

	var out []RAGASSample
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var s RAGASSample
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, errors.Wrapf(err, "decode ragas sample at %s:%d", path, line)
		}
		out = append(out, s)
	}
	if err := sc.Err(); err != nil {
		return nil, errors.Wrap(err, "scan ragas golden")
	}
	return out, nil
}

// TODO(eval): port from ragas v0.4.x source — see testdata/ragas_prompts/PROVENANCE.md.
const promptFaithfulness = `PLACEHOLDER faithfulness prompt
question: {{.Question}}
answer: {{.Answer}}
contexts: {{range .Contexts}}- {{.}}
{{end}}Return JSON {"score": <0..1>}`

// TODO(eval): port from ragas v0.4.x source.
const promptContextPrecision = `PLACEHOLDER context_precision prompt
question: {{.Question}}
contexts: {{range .Contexts}}- {{.}}
{{end}}reference: {{.ReferenceAnswer}}
Return JSON {"score": <0..1>}`

// TODO(eval): port from ragas v0.4.x source.
const promptContextRecall = `PLACEHOLDER context_recall prompt
reference: {{.ReferenceAnswer}}
contexts: {{range .Contexts}}- {{.}}
{{end}}Return JSON {"score": <0..1>}`

// TODO(eval): port from ragas v0.4.x source.
const promptContextEntitiesRecall = `PLACEHOLDER context_entities_recall prompt
reference_contexts: {{range .ReferenceContexts}}- {{.}}
{{end}}contexts: {{range .Contexts}}- {{.}}
{{end}}Return JSON {"score": <0..1>}`

// TODO(eval): port from ragas v0.4.x source.
const promptAnswerRelevancyRephrase = `PLACEHOLDER answer_relevancy rephrase prompt
answer: {{.Answer}}
Return JSON {"questions": ["..."]}`

// TODO(eval): port from ragas v0.4.x source.
const promptAnswerCorrectness = `PLACEHOLDER answer_correctness prompt
question: {{.Question}}
answer: {{.Answer}}
reference: {{.ReferenceAnswer}}
Return JSON {"score": <0..1>}`

// scoreSchema is reused by every score-style metric.
var scoreSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"score": map[string]any{"type": "number"},
	},
	"required": []string{"score"},
}

// rephraseSchema is the answer-relevancy rephrase response schema.
var rephraseSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"questions": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
	"required": []string{"questions"},
}

// Faithfulness scores how grounded the answer is in the provided contexts.
func Faithfulness(ctx context.Context, judge LLMJudge, opts RAGASOpts, sample RAGASSample) (float64, error) {
	return runScoreMetric(ctx, judge, opts, promptFaithfulness, sample)
}

// ContextPrecision scores ranking quality of retrieved contexts.
func ContextPrecision(ctx context.Context, judge LLMJudge, opts RAGASOpts, sample RAGASSample) (float64, error) {
	return runScoreMetric(ctx, judge, opts, promptContextPrecision, sample)
}

// ContextRecall scores whether the contexts cover the reference answer.
func ContextRecall(ctx context.Context, judge LLMJudge, opts RAGASOpts, sample RAGASSample) (float64, error) {
	return runScoreMetric(ctx, judge, opts, promptContextRecall, sample)
}

// ContextEntitiesRecall scores entity-level coverage of the reference contexts.
func ContextEntitiesRecall(ctx context.Context, judge LLMJudge, opts RAGASOpts, sample RAGASSample) (float64, error) {
	return runScoreMetric(ctx, judge, opts, promptContextEntitiesRecall, sample)
}

// AnswerCorrectness scores semantic correctness against the reference answer.
func AnswerCorrectness(ctx context.Context, judge LLMJudge, opts RAGASOpts, sample RAGASSample) (float64, error) {
	return runScoreMetric(ctx, judge, opts, promptAnswerCorrectness, sample)
}

// AnswerRelevancy: judge rephrases the answer back into N questions, score is
// mean cosine similarity of the embedded original question vs each rephrased
// question (RAGAS v0.4 behaviour).
func AnswerRelevancy(ctx context.Context, judge LLMJudge, embedder EmbeddingClient, opts RAGASOpts, sample RAGASSample) (float64, error) {
	if judge == nil {
		return 0, errors.New("judge is nil")
	}
	if embedder == nil {
		return 0, errors.New("embedder is nil")
	}

	prompt, err := renderPrompt(promptAnswerRelevancyRephrase, sample)
	if err != nil {
		return 0, errors.WithStack(err)
	}

	resp, err := judge.Judge(ctx, JudgeRequest{
		Model:        opts.Model,
		Prompt:       prompt,
		Schema:       rephraseSchema,
		MaxOutTokens: orDefault(opts.MaxOutTokens, 512),
		Temperature:  opts.Temperature,
	})
	if err != nil {
		return 0, errors.Wrap(err, "judge rephrase")
	}

	rephrased, err := extractStringSlice(resp.Output, "questions")
	if err != nil {
		return 0, errors.Wrap(err, "parse rephrased questions")
	}
	if len(rephrased) == 0 {
		return 0, nil
	}

	embeds, err := embedder.Embed(ctx, append([]string{sample.Question}, rephrased...))
	if err != nil {
		return 0, errors.Wrap(err, "embed")
	}
	if len(embeds) != len(rephrased)+1 {
		return 0, errors.Errorf("embedder returned %d vectors, expected %d", len(embeds), len(rephrased)+1)
	}

	base := embeds[0]
	sum := 0.0
	for i := 1; i < len(embeds); i++ {
		sum += cosine(base, embeds[i])
	}
	return sum / float64(len(rephrased)), nil
}

// RunRAGASEval drives all six metrics over a sample set.
// embedder may be nil; in that case AnswerRelevancy is reported as "skipped".
func RunRAGASEval(ctx context.Context, judge LLMJudge, embedder EmbeddingClient, samples []RAGASSample, opts RAGASOpts) (RAGASReport, error) {
	if judge == nil {
		return RAGASReport{
			Faithfulness:          RAGASMetricStats{Status: "skipped"},
			ContextPrecision:      RAGASMetricStats{Status: "skipped"},
			ContextRecall:         RAGASMetricStats{Status: "skipped"},
			ContextEntitiesRecall: RAGASMetricStats{Status: "skipped"},
			AnswerRelevancy:       RAGASMetricStats{Status: "skipped"},
			AnswerCorrectness:     RAGASMetricStats{Status: "skipped"},
		}, nil
	}

	type series struct {
		values []float64
	}
	collect := map[string]*series{
		"faithfulness":            {},
		"context_precision":       {},
		"context_recall":          {},
		"context_entities_recall": {},
		"answer_relevancy":        {},
		"answer_correctness":      {},
	}

	for _, s := range samples {
		if v, err := Faithfulness(ctx, judge, opts, s); err == nil {
			collect["faithfulness"].values = append(collect["faithfulness"].values, v)
		}
		if v, err := ContextPrecision(ctx, judge, opts, s); err == nil {
			collect["context_precision"].values = append(collect["context_precision"].values, v)
		}
		if v, err := ContextRecall(ctx, judge, opts, s); err == nil {
			collect["context_recall"].values = append(collect["context_recall"].values, v)
		}
		if v, err := ContextEntitiesRecall(ctx, judge, opts, s); err == nil {
			collect["context_entities_recall"].values = append(collect["context_entities_recall"].values, v)
		}
		if v, err := AnswerCorrectness(ctx, judge, opts, s); err == nil {
			collect["answer_correctness"].values = append(collect["answer_correctness"].values, v)
		}
		if embedder != nil {
			if v, err := AnswerRelevancy(ctx, judge, embedder, opts, s); err == nil {
				collect["answer_relevancy"].values = append(collect["answer_relevancy"].values, v)
			}
		}
	}

	rep := RAGASReport{
		Faithfulness:          stats(collect["faithfulness"].values, "ok"),
		ContextPrecision:      stats(collect["context_precision"].values, "ok"),
		ContextRecall:         stats(collect["context_recall"].values, "ok"),
		ContextEntitiesRecall: stats(collect["context_entities_recall"].values, "ok"),
		AnswerCorrectness:     stats(collect["answer_correctness"].values, "ok"),
	}
	if embedder == nil {
		rep.AnswerRelevancy = RAGASMetricStats{Status: "skipped"}
	} else {
		rep.AnswerRelevancy = stats(collect["answer_relevancy"].values, "ok")
	}
	return rep, nil
}

func runScoreMetric(ctx context.Context, judge LLMJudge, opts RAGASOpts, tmpl string, sample RAGASSample) (float64, error) {
	if judge == nil {
		return 0, errors.New("judge is nil")
	}
	prompt, err := renderPrompt(tmpl, sample)
	if err != nil {
		return 0, errors.WithStack(err)
	}
	resp, err := judge.Judge(ctx, JudgeRequest{
		Model:        opts.Model,
		Prompt:       prompt,
		Schema:       scoreSchema,
		MaxOutTokens: orDefault(opts.MaxOutTokens, 256),
		Temperature:  opts.Temperature,
	})
	if err != nil {
		return 0, errors.Wrap(err, "judge")
	}
	score, err := extractFloat(resp.Output, "score")
	if err != nil {
		return 0, errors.WithStack(err)
	}
	return score, nil
}

// helpers (renderPrompt, extractFloat, extractStringSlice, cosine, stats,
// orDefault) live in ragas_helpers.go.
