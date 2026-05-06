package eval

import (
	"context"
	"math"
	"testing"
)

type scriptedJudge struct {
	score float64
}

func (s scriptedJudge) Judge(ctx context.Context, req JudgeRequest) (JudgeResponse, error) {
	if req.Schema != nil {
		// answer_relevancy uses the rephrase schema with a "questions" field.
		if _, ok := req.Schema["properties"].(map[string]any)["questions"]; ok {
			return JudgeResponse{Output: map[string]any{
				"questions": []any{"rephrase-1", "rephrase-2"},
			}, TotalTokens: 10}, nil
		}
	}
	return JudgeResponse{Output: map[string]any{"score": s.score}, TotalTokens: 10}, nil
}

type scriptedEmbedder struct{}

func (scriptedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

func TestFaithfulnessParsesScore(t *testing.T) {
	got, err := Faithfulness(context.Background(), scriptedJudge{score: 0.87}, RAGASOpts{Model: "fake"}, RAGASSample{
		Question: "q", Answer: "a", Contexts: []string{"c1", "c2"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(got-0.87) > 1e-9 {
		t.Fatalf("faithfulness = %v, want 0.87", got)
	}
}

func TestRunRAGASEvalAggregates(t *testing.T) {
	samples := []RAGASSample{
		{Question: "q1", Answer: "a1", Contexts: []string{"c"}, ReferenceAnswer: "a1"},
		{Question: "q2", Answer: "a2", Contexts: []string{"c"}, ReferenceAnswer: "a2"},
	}
	rep, err := RunRAGASEval(context.Background(), scriptedJudge{score: 0.75}, scriptedEmbedder{}, samples, RAGASOpts{Model: "fake"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Faithfulness.Status != "ok" {
		t.Fatalf("faithfulness status = %q, want ok", rep.Faithfulness.Status)
	}
	if math.Abs(rep.Faithfulness.Mean-0.75) > 1e-9 {
		t.Fatalf("faithfulness mean = %v, want 0.75", rep.Faithfulness.Mean)
	}
	if rep.AnswerRelevancy.Status != "ok" {
		t.Fatalf("answer_relevancy status = %q, want ok", rep.AnswerRelevancy.Status)
	}
}

func TestRunRAGASEvalNilJudgeReturnsSkipped(t *testing.T) {
	rep, err := RunRAGASEval(context.Background(), nil, nil, nil, RAGASOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Faithfulness.Status != "skipped" {
		t.Fatalf("nil judge → faithfulness must be skipped, got %q", rep.Faithfulness.Status)
	}
	if rep.AnswerRelevancy.Status != "skipped" {
		t.Fatalf("nil judge → answer_relevancy must be skipped")
	}
}
