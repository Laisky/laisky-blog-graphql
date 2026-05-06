// Command promote-pageindex scores a Phase 3 shadow-replay JSONL log against
// the §7.8 promotion gate. It supports a stub judge for smoke-testing the
// wiring, a single-model OpenAI Responses-API judge, and an ensemble of two
// OpenAI judges with majority voting (per §7.8 position-bias mitigation).
//
// Secrets are read from the environment only; no --api-key flag is accepted.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	errors "github.com/Laisky/errors/v2"
	"golang.org/x/sync/errgroup"

	plugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/pageindex"
)

// secondaryEnsembleModel is the secondary model used by --judge=ensemble. The
// primary model is whatever the operator passes via --judge-model. Two
// different model families reduce shared bias per §7.8.
const secondaryEnsembleModel = "gpt-4o-mini"

// judgeJSONSchema is the strict Responses-API schema the OpenAI judge expects
// back. Strict mode lets us decode pageindex.Response.Output directly.
var judgeJSONSchema = json.RawMessage(`{"type":"object","properties":{"winner":{"type":"string","enum":["A","B","TIE"]},"reason":{"type":"string"}},"required":["winner","reason"],"additionalProperties":false}`)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "promote-pageindex:", err)
		os.Exit(1)
	}
}

func run() error {
	records := flag.String("records", "", "Path to a shadow-replay JSONL log (required).")
	judgeKind := flag.String("judge", "stub", `Judge implementation: "stub" (always picks A; smoke test only), "openai" (single-model Responses-API judge), or "ensemble" (two OpenAI judges with majority vote, secondary model `+secondaryEnsembleModel+`).`)
	judgeModel := flag.String("judge-model", "gpt-5.4-mini", "Primary model used by --judge=openai|ensemble.")
	judgeBaseURL := flag.String("judge-base-url", "", "Optional Responses-API base URL override (vendors that re-implement the API). Empty = api.openai.com.")
	flag.Parse()

	if *records == "" {
		flag.Usage()
		return errors.New("--records is required")
	}

	searches, err := loadSearches(*records)
	if err != nil {
		return err
	}

	judge, err := makeJudge(*judgeKind, *judgeModel, *judgeBaseURL)
	if err != nil {
		return err
	}

	res, err := plugin.ScoreShadowReplay(context.Background(), searches, judge, plugin.ScoreOpts{})
	if err != nil {
		return err
	}
	fmt.Print(renderMarkdown(res))
	return nil
}

func loadSearches(path string) ([]plugin.SearchRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.Wrap(err, "open records")
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var out []plugin.SearchRecord
	for scanner.Scan() {
		var env struct {
			Kind   string               `json:"kind"`
			Search *plugin.SearchRecord `json:"search,omitempty"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
			return nil, errors.Wrap(err, "parse record")
		}
		if env.Kind == "search" && env.Search != nil {
			out = append(out, *env.Search)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.Wrap(err, "read records")
	}
	return out, nil
}

type stubJudge struct{}

func (stubJudge) CompareResults(_ context.Context, _ string, _, _ plugin.SearchResult) (plugin.Verdict, error) {
	return plugin.Verdict{Winner: "A", Reason: "stub judge"}, nil
}

// makeJudge builds the requested Judge. For openai/ensemble we read
// OPENAI_API_KEY from the environment (per repo convention; secrets are never
// passed as flags) and log only the SHA-256 prefix.
func makeJudge(kind, model, baseURL string) (plugin.Judge, error) {
	switch kind {
	case "stub":
		return stubJudge{}, nil
	case "openai":
		llm, err := newOpenAIJudgeLLM(model, baseURL)
		if err != nil {
			return nil, err
		}
		return &openaiJudge{llm: llm, model: model}, nil
	case "ensemble":
		primary, err := newOpenAIJudgeLLM(model, baseURL)
		if err != nil {
			return nil, errors.Wrap(err, "primary judge")
		}
		secondary, err := newOpenAIJudgeLLM(secondaryEnsembleModel, baseURL)
		if err != nil {
			return nil, errors.Wrap(err, "secondary judge")
		}
		return &ensembleJudge{
			judges: []*openaiJudge{
				{llm: primary, model: model},
				{llm: secondary, model: secondaryEnsembleModel},
			},
		}, nil
	default:
		return nil, errors.Errorf("unknown judge %q (allowed: stub, openai, ensemble)", kind)
	}
}

// newOpenAIJudgeLLM constructs a pageindex.LLM bound to OPENAI_API_KEY. The
// API key is read from the env (never a flag) and only its SHA-256 prefix is
// echoed to stderr for operator-visible identification.
func newOpenAIJudgeLLM(model, baseURL string) (pageindex.LLM, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, errors.New("OPENAI_API_KEY is required for --judge=openai")
	}
	sum := sha256.Sum256([]byte(apiKey))
	fmt.Fprintf(os.Stderr, "promote-pageindex: judge api_key sha256=%s model=%s\n", hex.EncodeToString(sum[:4]), model)
	llm, err := pageindex.NewOpenAILLM(pageindex.LLMConfig{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
	})
	if err != nil {
		return nil, errors.Wrap(err, "construct openai llm")
	}
	return llm, nil
}

// openaiJudge implements plugin.Judge by asking an OpenAI Responses-API model
// to pick the better SearchResult. Position-swap bias mitigation lives in the
// score caller (ScoreShadowReplay); this judge MUST NOT swap positions itself.
type openaiJudge struct {
	llm   pageindex.LLM
	model string
}

// judgeReply is the strict-JSON shape we ask the Responses API to return.
type judgeReply struct {
	Winner string `json:"winner"`
	Reason string `json:"reason"`
}

const judgeSystemPrompt = `You are an offline relevance judge for a code/document search system. Given a user query and two candidate result sets (A and B), pick the set that best answers the query. Prefer sets whose chunks directly satisfy the query intent, contain higher-quality context, and avoid off-topic content. If both sets are equally good (or equally bad), reply with TIE. Reply in strict JSON matching the schema: {"winner":"A|B|TIE","reason":"<one sentence>"}.`

// CompareResults builds the judge prompt and parses the structured reply.
// On parse failure, the verdict is TIE so the call still counts in the score
// while the operator sees the underlying error in stderr (and the wrapped err).
func (j *openaiJudge) CompareResults(ctx context.Context, query string, a, b plugin.SearchResult) (plugin.Verdict, error) {
	user := buildJudgeUserPrompt(query, a, b)
	req := pageindex.Request{
		Model: j.model,
		Input: []pageindex.InputItem{
			{Role: "system", Content: judgeSystemPrompt},
			{Role: "user", Content: user},
		},
		Schema:       judgeJSONSchema,
		SchemaName:   "shadow_judge_verdict",
		MaxOutTokens: 512,
		Temperature:  0,
	}
	resp, err := j.llm.Respond(ctx, req)
	if err != nil {
		return plugin.Verdict{Winner: "TIE", Reason: "judge call failed: " + err.Error()}, errors.Wrap(err, "judge call")
	}
	var out judgeReply
	payload := resp.Output
	if len(payload) == 0 {
		payload = json.RawMessage(resp.Text)
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		fmt.Fprintf(os.Stderr, "promote-pageindex: judge response unparseable model=%s err=%v body=%q\n", j.model, err, resp.Text)
		return plugin.Verdict{Winner: "TIE", Reason: "judge response unparseable: " + err.Error()}, errors.Wrap(err, "decode judge reply")
	}
	winner := strings.ToUpper(strings.TrimSpace(out.Winner))
	switch winner {
	case "A", "B", "TIE":
	default:
		return plugin.Verdict{Winner: "TIE", Reason: "judge returned unknown winner: " + out.Winner}, errors.Errorf("unknown winner %q", out.Winner)
	}
	return plugin.Verdict{Winner: winner, Reason: out.Reason}, nil
}

// buildJudgeUserPrompt renders the query and both candidate result sets as a
// deterministic numbered chunk list. The first 5 chunks per side are kept and
// each chunk's content is truncated to 200 chars to bound prompt size.
func buildJudgeUserPrompt(query string, a, b plugin.SearchResult) string {
	const maxChunks = 5
	const maxChunkChars = 200
	var sb strings.Builder
	sb.WriteString("Query:\n")
	sb.WriteString(query)
	sb.WriteString("\n\nCandidate A:\n")
	renderChunks(&sb, a, maxChunks, maxChunkChars)
	sb.WriteString("\nCandidate B:\n")
	renderChunks(&sb, b, maxChunks, maxChunkChars)
	sb.WriteString("\nPick the better candidate (A, B, or TIE) and reply in strict JSON.")
	return sb.String()
}

func renderChunks(sb *strings.Builder, res plugin.SearchResult, maxChunks, maxChunkChars int) {
	if len(res.Chunks) == 0 {
		sb.WriteString("(no results)\n")
		return
	}
	for i, c := range res.Chunks {
		if i >= maxChunks {
			fmt.Fprintf(sb, "... (%d more chunks truncated)\n", len(res.Chunks)-maxChunks)
			break
		}
		content := c.ChunkContent
		if len(content) > maxChunkChars {
			content = content[:maxChunkChars] + "..."
		}
		// Newlines inside a chunk are collapsed so the numbered list is readable.
		content = strings.ReplaceAll(content, "\n", " ")
		fmt.Fprintf(sb, "%d. path=%s score=%.4f content=%s\n", i+1, c.FilePath, c.Score, content)
	}
}

// ensembleJudge runs N openaiJudge instances in parallel and majority-votes.
// With 2 judges: agreement → that winner; disagreement → TIE.
type ensembleJudge struct {
	judges []*openaiJudge
}

func (e *ensembleJudge) CompareResults(ctx context.Context, query string, a, b plugin.SearchResult) (plugin.Verdict, error) {
	verdicts := make([]plugin.Verdict, len(e.judges))
	g, gctx := errgroup.WithContext(ctx)
	for i, j := range e.judges {
		i, j := i, j
		g.Go(func() error {
			v, err := j.CompareResults(gctx, query, a, b)
			verdicts[i] = v
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return plugin.Verdict{Winner: "TIE", Reason: "ensemble call failed: " + err.Error()}, errors.Wrap(err, "ensemble judge")
	}
	return tallyEnsembleVerdicts(verdicts, e.judges), nil
}

// tallyEnsembleVerdicts implements the §7.8 majority-vote rule for two
// judges: agree → that winner; disagree → TIE. The Reason concatenates each
// judge's reason with its model name. Exposed (lowercase) for unit tests.
func tallyEnsembleVerdicts(verdicts []plugin.Verdict, judges []*openaiJudge) plugin.Verdict {
	counts := map[string]int{}
	for _, v := range verdicts {
		counts[v.Winner]++
	}
	winner := "TIE"
	best := 0
	tied := false
	for w, n := range counts {
		switch {
		case n > best:
			winner = w
			best = n
			tied = false
		case n == best:
			tied = true
		}
	}
	if tied {
		winner = "TIE"
	}
	parts := make([]string, 0, len(verdicts))
	for i, v := range verdicts {
		model := ""
		if i < len(judges) && judges[i] != nil {
			model = judges[i].model
		}
		parts = append(parts, fmt.Sprintf("%s=%s (%s)", model, v.Winner, v.Reason))
	}
	return plugin.Verdict{Winner: winner, Reason: strings.Join(parts, " | ")}
}

func renderMarkdown(res plugin.ScoreResult) string {
	return fmt.Sprintf(`# Shadow-replay promotion gate

| Metric        | Value |
| ------------- | ----- |
| Queries       | %d |
| Live wins (A) | %d |
| Shadow wins (B) | %d |
| Ties          | %d |
| A win-rate    | %.4f |
| B win-rate    | %.4f |
| p-value       | %.4f |
| Decision      | %s |
`,
		res.NQueries, res.AWins, res.BWins, res.Ties,
		res.AWinRate, res.BWinRate, res.PValue, res.PromotionDecision,
	)
}
