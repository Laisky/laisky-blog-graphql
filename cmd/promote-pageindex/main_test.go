package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	plugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/pageindex"
)

func TestStubJudgeAlwaysPicksA(t *testing.T) {
	t.Parallel()
	v, err := stubJudge{}.CompareResults(context.Background(), "q", files.SearchResult{}, files.SearchResult{})
	if err != nil {
		t.Fatalf("stub judge returned error: %v", err)
	}
	if v.Winner != "A" {
		t.Fatalf("stub judge winner = %q, want A", v.Winner)
	}
}

// fakeLLM is a deterministic pageindex.LLM that returns a scripted Responses
// payload regardless of request. Tests use it to script the openaiJudge.
type fakeLLM struct {
	winner string
	reason string
	err    error
}

func (f *fakeLLM) Respond(_ context.Context, _ pageindex.Request) (*pageindex.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	body, _ := json.Marshal(judgeReply{Winner: f.winner, Reason: f.reason})
	return &pageindex.Response{Output: body, Text: string(body)}, nil
}

func (f *fakeLLM) CountTokens(_ context.Context, _ pageindex.Request) (int, error) { return 0, nil }

func TestEnsembleJudgeMajorityVote(t *testing.T) {
	t.Parallel()
	judges := []*openaiJudge{
		{llm: &fakeLLM{winner: "B", reason: "primary picks B"}, model: "primary-model"},
		{llm: &fakeLLM{winner: "B", reason: "secondary picks B"}, model: "secondary-model"},
	}
	e := &ensembleJudge{judges: judges}
	v, err := e.CompareResults(context.Background(), "query", files.SearchResult{}, files.SearchResult{})
	if err != nil {
		t.Fatalf("ensemble judge error: %v", err)
	}
	if v.Winner != "B" {
		t.Fatalf("ensemble winner = %q, want B", v.Winner)
	}
	if !strings.Contains(v.Reason, "primary-model") || !strings.Contains(v.Reason, "secondary-model") {
		t.Fatalf("ensemble reason missing model names: %q", v.Reason)
	}
	if !strings.Contains(v.Reason, "primary picks B") {
		t.Fatalf("ensemble reason missing primary's reason: %q", v.Reason)
	}
}

func TestEnsembleJudgeSplitVoteIsTie(t *testing.T) {
	t.Parallel()
	judges := []*openaiJudge{
		{llm: &fakeLLM{winner: "A", reason: "primary picks A"}, model: "primary-model"},
		{llm: &fakeLLM{winner: "B", reason: "secondary picks B"}, model: "secondary-model"},
	}
	e := &ensembleJudge{judges: judges}
	v, err := e.CompareResults(context.Background(), "query", files.SearchResult{}, files.SearchResult{})
	if err != nil {
		t.Fatalf("ensemble judge error: %v", err)
	}
	if v.Winner != "TIE" {
		t.Fatalf("split-vote winner = %q, want TIE", v.Winner)
	}
}

func TestRenderMarkdownContainsAllFields(t *testing.T) {
	t.Parallel()
	res := plugin.ScoreResult{
		NQueries:          120,
		AWins:             40,
		BWins:             70,
		Ties:              10,
		AWinRate:          0.375,
		BWinRate:          0.625,
		PValue:            0.0123,
		PromotionDecision: plugin.DecisionPromoteB,
	}
	got := renderMarkdown(res)
	wants := []string{
		"Shadow-replay promotion gate",
		"| Queries       | 120 |",
		"| Live wins (A) | 40 |",
		"| Shadow wins (B) | 70 |",
		"| Ties          | 10 |",
		"| A win-rate    | 0.3750 |",
		"| B win-rate    | 0.6250 |",
		"| p-value       | 0.0123 |",
		"| Decision      | " + plugin.DecisionPromoteB + " |",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("renderMarkdown output missing %q\nfull output:\n%s", w, got)
		}
	}
}

func TestOpenAIJudgeUnparseableResponseReturnsTie(t *testing.T) {
	t.Parallel()
	// fakeLLM with junk Output that does not match the schema.
	bad := &badLLM{text: "this is not json"}
	j := &openaiJudge{llm: bad, model: "test-model"}
	v, err := j.CompareResults(context.Background(), "q", files.SearchResult{}, files.SearchResult{})
	if err == nil {
		t.Fatal("expected wrapped parse error, got nil")
	}
	if v.Winner != "TIE" {
		t.Fatalf("unparseable verdict winner = %q, want TIE", v.Winner)
	}
	if !strings.Contains(v.Reason, "unparseable") {
		t.Fatalf("verdict reason missing 'unparseable': %q", v.Reason)
	}
}

type badLLM struct{ text string }

func (b *badLLM) Respond(_ context.Context, _ pageindex.Request) (*pageindex.Response, error) {
	return &pageindex.Response{Output: json.RawMessage(b.text), Text: b.text}, nil
}
func (b *badLLM) CountTokens(_ context.Context, _ pageindex.Request) (int, error) { return 0, nil }

func TestBuildJudgeUserPromptCapsChunksAndChars(t *testing.T) {
	t.Parallel()
	bigContent := strings.Repeat("x", 1000)
	res := files.SearchResult{
		Chunks: []files.ChunkEntry{
			{FilePath: "p1", Score: 0.9, ChunkContent: bigContent},
			{FilePath: "p2"},
			{FilePath: "p3"},
			{FilePath: "p4"},
			{FilePath: "p5"},
			{FilePath: "p6"}, // beyond cap
			{FilePath: "p7"},
		},
	}
	prompt := buildJudgeUserPrompt("hello query", res, files.SearchResult{})
	if !strings.Contains(prompt, "hello query") {
		t.Errorf("prompt missing query: %s", prompt)
	}
	if strings.Contains(prompt, "p6") {
		t.Errorf("prompt should drop chunks past the cap, got: %s", prompt)
	}
	if !strings.Contains(prompt, "more chunks truncated") {
		t.Errorf("prompt should announce truncation: %s", prompt)
	}
	// The 1000-char content must be truncated to 200 chars + ellipsis.
	if strings.Contains(prompt, strings.Repeat("x", 300)) {
		t.Errorf("prompt should truncate chunk content to 200 chars: %s", prompt)
	}
}
