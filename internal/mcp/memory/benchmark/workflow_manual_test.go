package benchmark

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// workflowPolicySource reads real repository configuration, not a duplicate fixture.
func workflowPolicySource(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", ".github", "workflows", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestBenchmarkWorkflowsManualOnly guards the event surface, including the capture
// workflow. Require a single conventional block-form on section so inline events,
// aliases, duplicate keys or newly added automatic event entries fail the policy.
// This is a policy check, not a replacement for validating complete YAML syntax.
func TestBenchmarkWorkflowsManualOnly(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"memory-benchmark.yml", "memory-benchmark-results.yml",
		"memory-benchmark-capture.yml", "eval-nightly.yml",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			source := workflowPolicySource(t, name)
			headers := regexp.MustCompile(`(?m)^on:.*$`).FindAllStringIndex(source, -1)
			if len(headers) != 1 || source[headers[0][0]:headers[0][1]] != "on:" {
				t.Fatal("expected one block-form on section")
			}
			var events []string
			for _, line := range strings.Split(source[headers[0][1]:], "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
				if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
					break
				}
				if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") {
					events = append(events, trimmed)
				}
			}
			if len(events) != 1 || events[0] != "workflow_dispatch:" {
				t.Fatalf("benchmarks must be manual-only; found events %v", events)
			}
			for _, forbidden := range []string{
				"context.payload.pull_request", "github.event.pull_request", "sparse-checkout:",
			} {
				if strings.Contains(source, forbidden) {
					t.Errorf("manual full-ref workflow must not depend on %q", forbidden)
				}
			}
		})
	}
}

func TestBenchmarkLiveRunRequiresOptIn(t *testing.T) {
	t.Parallel()
	source := workflowPolicySource(t, "memory-benchmark.yml")
	input := regexp.MustCompile(`(?m)^      run_live:\n(?:        .*\n)*        default: false\n        type: boolean`)
	if !input.MatchString(source) || !strings.Contains(source, "  live:\n    if: inputs.run_live\n") {
		t.Fatal("deployed benchmarks must require an explicit false-by-default input")
	}
}

func TestBenchmarkManualResultsProtectPRTarget(t *testing.T) {
	t.Parallel()
	source := workflowPolicySource(t, "memory-benchmark-results.yml")
	for _, required := range []string{
		"      pr_number:\n", "        default: ''\n",
		"        if: inputs.pr_number != ''\n", "PR_NUMBER: ${{ inputs.pr_number }}",
		"Number.isSafeInteger(issue_number)", "pull.head.sha !== report.run.git_sha",
		"pull.head.repo?.full_name", "comment.user?.login === 'github-actions[bot]'",
		"          persist-credentials: false\n",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("manual scorecard publication is missing %q", required)
		}
	}
}
