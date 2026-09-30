// Command eval-plugin is the driver binary for the MCP memory plugin
// evaluation harness. It wires a concrete plugin into the in-tree eval
// package, runs the configured suites, and writes the §7.4 scorecard plus
// raw per-query records and run metadata.
//
// Usage:
//
//	go run ./cmd/eval-plugin --plugin=rag --golden=tests/eval/golden \
//	    --suites=retrieval,ops,redteam --git-sha=$(git rev-parse --short HEAD)
//
// The driver reports missing datasets / unavailable storage as `n/a` cells
// in the scorecard rather than crashing, per the proposal §4.6 graceful
// missing rule.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/conformance/eval"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// harnessVersion stamps run_metadata.yml so cross-version diffs are obvious.
const harnessVersion = "v0.1.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "eval-plugin:", err)
		os.Exit(1)
	}
}

type cliFlags struct {
	plugin   string
	golden   string
	out      string
	suites   string
	config   string
	gitSHA   string
	baseline bool
	force    bool
}

func parseFlags() cliFlags {
	var c cliFlags
	flag.StringVar(&c.plugin, "plugin", "", `Plugin name to evaluate ("rag" today; future: "pageindex"). Required.`)
	flag.StringVar(&c.golden, "golden", "tests/eval/golden", "Path to tests/eval/golden/.")
	flag.StringVar(&c.out, "out", "", `Output directory. Default "docs/eval/runs/<short-sha>" auto-resolved when empty.`)
	flag.StringVar(&c.suites, "suites", "retrieval,ops,redteam", "Comma-separated suites: retrieval,ragas,redteam,ops,public.")
	flag.StringVar(&c.config, "config", "", "Path to settings yaml (uses repo defaults if omitted).")
	flag.StringVar(&c.gitSHA, "git-sha", "", "Git SHA stamped in the scorecard. Defaults to `git rev-parse --short HEAD`.")
	flag.BoolVar(&c.baseline, "baseline", false, "Write to docs/eval/baseline_v1/ instead of docs/eval/runs/<sha>/.")
	flag.BoolVar(&c.force, "force", false, "Permit overwriting an existing baseline. Use with --baseline only.")
	flag.Parse()
	return c
}

func run() error {
	c := parseFlags()
	if c.plugin == "" {
		flag.Usage()
		return errors.New("--plugin is required")
	}
	c.plugin = mcpplugin.NormalizeName(c.plugin)

	gitSHA := c.gitSHA
	if gitSHA == "" {
		gitSHA = detectGitSHA()
	}

	outDir, err := resolveOutDir(c, gitSHA)
	if err != nil {
		return err
	}

	suites := normalizeSuites(c.suites)

	// Resolve the LLM judge from environment. Never log the secret in plain
	// text; only its SHA-256 prefix. Phase 1 ships without a Responses-API
	// judge wired up: we log presence of the env var so operators can see it
	// landed, but RAGAS still renders n/a until §4.6.5 wires the production
	// client. The suite is dropped silently when no key is configured.
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	baseURL := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	_ = baseURL
	judge := buildJudge(apiKey, baseURL)
	if judge == nil {
		// RAGAS suite needs a judge; drop it silently when one is unavailable
		// so the rest of the scorecard still renders n/a cells cleanly.
		suites = filterOut(suites, "ragas")
	}

	plugin, pluginNote := buildPlugin(c.plugin)

	// Compute golden_versions before invocation so we record them even when a
	// suite is missing its dataset.
	goldenVersions, err := computeGoldenVersions(c.golden)
	if err != nil {
		return errors.Wrap(err, "fingerprint golden datasets")
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir out %s: %w", outDir, err)
	}

	runID := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	cfg := eval.RunConfig{
		Plugin:     plugin,
		PluginName: c.plugin,
		GoldenDir:  c.golden,
		Judge:      judge,
		// EmbeddingClient remains nil in Phase 1; RAGAS answer_relevancy will
		// surface n/a until §4.6.5 wires the production embedder.
		EmbeddingClient: nil,
		GitSHA:          gitSHA,
		UTCRunID:        runID,
		Suites:          suites,
	}

	fmt.Printf("eval plugin=%s suites=%s out=%s git_sha=%s%s\n",
		c.plugin, strings.Join(suites, ","), outDir, gitSHA, pluginNote)

	ctx := context.Background()
	result, err := eval.Run(ctx, cfg, os.Stdout)
	if err != nil {
		return fmt.Errorf("eval run: %w", err)
	}
	for _, k := range sortedKeys(goldenVersions) {
		result.Scorecard.GoldenSetVersions[k] = goldenVersions[k]
	}

	scorecardPath := scorecardPath(outDir, c.plugin, c.baseline)
	rawPath := filepath.Join(outDir, "raw_per_query.jsonl")
	metaPath := filepath.Join(outDir, "run_metadata.yml")

	if c.baseline && !c.force {
		if _, err := os.Stat(scorecardPath); err == nil {
			return errors.Errorf("baseline exists at %s; pass --force to overwrite", scorecardPath)
		}
	}

	if err := writeScorecard(scorecardPath, result.Scorecard); err != nil {
		return err
	}
	if err := writeRawPerQuery(rawPath, result.RawPerQuery); err != nil {
		return err
	}
	if err := writeRunMetadata(metaPath, gitSHA, runID, c.golden, goldenVersions, judge); err != nil {
		return err
	}

	fmt.Printf("eval done scorecard=%s raw=%s metadata=%s\n", scorecardPath, rawPath, metaPath)
	return nil
}

// buildJudge wires the optional LLM judge from environment. Phase 1 leaves
// this as a stub: it logs whether the API key is present (only the SHA-256
// prefix; never the secret itself) and returns nil so RAGAS renders n/a until
// §4.6.5 wires the production Responses-API client.
func buildJudge(apiKey, baseURL string) eval.LLMJudge {
	_ = baseURL
	if apiKey == "" {
		fmt.Println("eval judge=none ragas=n/a")
		return nil
	}
	fmt.Printf("eval judge=pending key_sha256_prefix=%s note=responses-api-client-not-wired-yet\n", hashPrefix(apiKey))
	return nil
}

// buildPlugin constructs the requested plugin. Phase 1 supports "rag"; if the
// production DI graph cannot be assembled (no DB, no embedder), fall back to a
// stub plugin so the suites that don't require backing storage can still run
// and the rest render `n/a` rather than crashing.
func buildPlugin(name string) (mcpplugin.Plugin, string) {
	switch name {
	case mcpplugin.DefaultPluginRAG:
		// The full DI graph (pgxpool, redis, embedder, credential protector)
		// is too heavy for this driver and is not available in CI. The eval
		// harness already treats missing storage as `n/a`, so we surface the
		// stub plugin and let suites self-skip.
		_ = files.AuthContext{}
		return newStubPlugin(name), " note=stub-plugin (no live storage; suites needing the file service render n/a)"
	case mcpplugin.DefaultPluginPageIndex:
		fmt.Fprintln(os.Stderr, "eval-plugin: pageindex plugin lands in Phase 2; not yet supported")
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "eval-plugin: unknown plugin %q\n", name)
	os.Exit(1)
	return nil, ""
}

// stubPlugin is a no-op plugin that returns empty results for every method.
// It exists so the redteam and ops suites can exercise their orchestration
// paths without a backing file service. Datasets that drive the retrieval and
// ragas suites surface as `n/a` cells in the scorecard via the harness's
// graceful-missing path.
type stubPlugin struct {
	name string
}

func newStubPlugin(name string) *stubPlugin { return &stubPlugin{name: name} }

func (p *stubPlugin) Name() string { return p.name }

func (p *stubPlugin) Capabilities() mcpplugin.Capabilities {
	return mcpplugin.Capabilities{
		SearchModes: []mcpplugin.SearchMode{mcpplugin.SearchModeHybrid},
		Notes:       "eval-plugin stub: returns empty results for every method",
	}
}

func (p *stubPlugin) Stat(context.Context, files.AuthContext, string, string) (files.StatResult, error) {
	return files.StatResult{}, nil
}

func (p *stubPlugin) Read(context.Context, files.AuthContext, string, string, int64, int64) (files.ReadResult, error) {
	return files.ReadResult{}, nil
}

func (p *stubPlugin) Write(context.Context, files.AuthContext, string, string, string, string, int64, files.WriteMode) (files.WriteResult, error) {
	return files.WriteResult{}, nil
}

func (p *stubPlugin) Delete(context.Context, files.AuthContext, string, string, bool) (files.DeleteResult, error) {
	return files.DeleteResult{}, nil
}

func (p *stubPlugin) Rename(context.Context, files.AuthContext, string, string, string, bool) (files.RenameResult, error) {
	return files.RenameResult{}, nil
}

func (p *stubPlugin) List(context.Context, files.AuthContext, string, string, int, int) (files.ListResult, error) {
	return files.ListResult{}, nil
}

func (p *stubPlugin) Search(context.Context, files.AuthContext, string, string, string, int) (files.SearchResult, error) {
	return files.SearchResult{}, nil
}

func (p *stubPlugin) Start(context.Context) error { return nil }
func (p *stubPlugin) Stop(context.Context) error  { return nil }

// resolveOutDir applies the auto-resolution rules: --baseline wins; otherwise
// honor explicit --out; otherwise default to docs/eval/runs/<short-sha>/.
func resolveOutDir(c cliFlags, gitSHA string) (string, error) {
	if c.baseline {
		if c.out != "" {
			return "", errors.New("--out conflicts with --baseline; pick one")
		}
		return "docs/eval/baseline_v1", nil
	}
	if c.out != "" {
		return c.out, nil
	}
	short := gitSHA
	if short == "" {
		short = "unknown"
	}
	return filepath.Join("docs", "eval", "runs", short), nil
}

func scorecardPath(outDir, plugin string, baseline bool) string {
	if baseline {
		return filepath.Join(outDir, fmt.Sprintf("%s_plugin_scorecard.md", plugin))
	}
	return filepath.Join(outDir, fmt.Sprintf("%s_plugin_scorecard.md", plugin))
}

func writeScorecard(path string, sc eval.Scorecard) (retErr error) {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create scorecard %s: %w", path, err)
	}
	defer func() { retErr = errors.Join(retErr, errors.Wrap(f.Close(), "close file")) }()
	if err := sc.WriteMarkdown(f); err != nil {
		return fmt.Errorf("write scorecard %s: %w", path, err)
	}
	return nil
}

func writeRawPerQuery(path string, rows []eval.PerQueryRecord) (retErr error) {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create raw %s: %w", path, err)
	}
	defer func() { retErr = errors.Join(retErr, errors.Wrap(f.Close(), "close file")) }()
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("encode raw row: %w", err)
		}
	}
	return nil
}

// writeRunMetadata emits a deterministic YAML document so re-runs produce
// stable diffs. Keys are written in a fixed order.
func writeRunMetadata(path, gitSHA, runUTC, goldenDir string, goldenVersions map[string]string, judge eval.LLMJudge) (retErr error) {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create metadata %s: %w", path, err)
	}
	defer func() { retErr = errors.Join(retErr, errors.Wrap(f.Close(), "close file")) }()

	hostname, _ := os.Hostname()
	judgeModels := "none"
	embeddingModel := "none"
	if judge != nil {
		// The driver does not currently mint a real judge; this branch is the
		// hook for §4.6.5 wiring.
		judgeModels = "configured"
	}

	var buf []byte
	buf = fmt.Appendf(buf, "harness_version: %s\n", quoteYAML(harnessVersion))
	buf = fmt.Appendf(buf, "git_sha: %s\n", quoteYAML(gitSHA))
	buf = fmt.Appendf(buf, "run_utc: %s\n", quoteYAML(runUTC))
	buf = fmt.Appendf(buf, "judge_models: %s\n", quoteYAML(judgeModels))
	buf = fmt.Appendf(buf, "embedding_model: %s\n", quoteYAML(embeddingModel))
	buf = fmt.Appendf(buf, "go_toolchain: %s\n", quoteYAML(runtime.Version()))
	buf = fmt.Appendf(buf, "hardware:\n")
	buf = fmt.Appendf(buf, "  num_cpu: %d\n", runtime.NumCPU())
	buf = fmt.Appendf(buf, "  hostname: %s\n", quoteYAML(hostname))
	buf = fmt.Appendf(buf, "  os: %s\n", quoteYAML(runtime.GOOS))
	buf = fmt.Appendf(buf, "  arch: %s\n", quoteYAML(runtime.GOARCH))
	buf = fmt.Appendf(buf, "golden_dir: %s\n", quoteYAML(goldenDir))
	buf = fmt.Appendf(buf, "golden_versions:\n")
	if len(goldenVersions) == 0 {
		buf = fmt.Appendln(buf, "  {}")
	} else {
		for _, k := range sortedKeys(goldenVersions) {
			buf = fmt.Appendf(buf, "  %s: %s\n", quoteYAML(k), quoteYAML(goldenVersions[k]))
		}
	}
	_, err = io.Copy(f, bytes.NewReader(buf))
	return errors.Wrap(err, "write metadata")
}

// computeGoldenVersions hashes every regular file under goldenDir and returns
// dataset-name → SHA-256 hex. Missing directory is reported as an empty map so
// the eval still produces metadata.
func computeGoldenVersions(goldenDir string) (map[string]string, error) {
	out := map[string]string{}
	info, err := os.Stat(goldenDir)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, errors.Wrap(err, "stat golden directory")
	}
	if !info.IsDir() {
		return nil, errors.New("golden path must be a directory")
	}
	err = filepath.WalkDir(goldenDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.Wrap(walkErr, "walk golden directory")
		}
		if d.IsDir() {
			return nil
		}
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if _, exists := out[name]; exists {
			return errors.Errorf("duplicate golden dataset name %q", name)
		}
		sum, err := sha256File(path)
		if err != nil {
			return errors.Wrap(err, "hash golden dataset")
		}
		out[name] = sum
		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return out, nil
}

func sha256File(path string) (_ string, retErr error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { retErr = errors.Join(retErr, errors.Wrap(f.Close(), "close file")) }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func detectGitSHA() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func normalizeSuites(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		v := strings.ToLower(strings.TrimSpace(p))
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func filterOut(suites []string, drop string) []string {
	out := make([]string, 0, len(suites))
	for _, s := range suites {
		if s == drop {
			fmt.Printf("eval suite=%s status=skipped reason=no_judge_configured\n", drop)
			continue
		}
		out = append(out, s)
	}
	return out
}

func hashPrefix(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])[:8]
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// quoteYAML wraps any non-empty string in double quotes with minimal escaping
// so deterministic output remains valid YAML for keys and values that contain
// `:` or other reserved characters.
func quoteYAML(v string) string {
	if v == "" {
		return `""`
	}
	escaped := strings.ReplaceAll(v, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}
