package search

import (
	"context"
	"testing"

	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// formatRecorder captures the output format each entry point asked the shared
// crawler for, so a divergence between MCP and GraphQL is visible.
type formatRecorder struct {
	requested []bool
	body      []byte
}

func (r *formatRecorder) fetch(_ context.Context, _ *rlibs.DB, _, _ string, outputMarkdown bool) ([]byte, error) {
	r.requested = append(r.requested, outputMarkdown)
	return r.body, nil
}

// TestFetchOutputFormatParityAcrossMCPAndGraphQL is the G03 contract: the
// output-format selection is a property of the shared fetch operation, not of
// one transport. GraphQL must be able to request raw HTML exactly as MCP can,
// must default to Markdown when the caller says nothing, and must report back
// which format it actually requested so a client never has to guess.
func TestFetchOutputFormatParityAcrossMCPAndGraphQL(t *testing.T) {
	const target = "https://8.8.8.8/"

	for _, tc := range []struct {
		name     string
		selected *bool
		expected bool
	}{
		{name: "omitted defaults to markdown", selected: nil, expected: true},
		{name: "markdown requested explicitly", selected: boolPtr(true), expected: true},
		{name: "raw html requested explicitly", selected: boolPtr(false), expected: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rdb := &rlibs.DB{}
			recorder := &formatRecorder{body: []byte("page")}
			resolver := NewMutationResolver(nil, rdb, nil)
			billing := func(context.Context, string, oneapi.Price, string) error { return nil }
			resolver.billingChecker, resolver.fetcher = billing, recorder.fetch

			ctx := contractContext(t)
			result, err := resolver.WebFetch(ctx, target, tc.selected)
			require.NoError(t, err)
			require.Equal(t, "page", result.Content)
			require.Equal(t, tc.expected, result.OutputMarkdown,
				"the response must state the format it actually requested")

			mcpRecorder := &formatRecorder{body: []byte("page")}
			tool, err := tools.NewWebFetchTool(rdb, log.Logger,
				func(context.Context) string { return "sk-contract-test-only" }, billing, mcpRecorder.fetch)
			require.NoError(t, err)
			arguments := map[string]any{"url": target}
			if tc.selected != nil {
				arguments["output_markdown"] = *tc.selected
			}
			toolResult, err := tool.Handle(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: arguments}})
			require.NoError(t, err)
			require.False(t, toolResult.IsError)

			require.Equal(t, []bool{tc.expected}, recorder.requested, "GraphQL requested the wrong format")
			require.Equal(t, recorder.requested, mcpRecorder.requested,
				"MCP and GraphQL must request the same format for the same caller intent")
		})
	}
}

// TestFetchOutputFormatIsAudited keeps the audit row honest: the recorded
// parameters must show the format that was actually requested, because the
// billed operation differs between a rendered Markdown conversion and raw HTML.
func TestFetchOutputFormatIsAudited(t *testing.T) {
	rdb := &rlibs.DB{}
	recorder := &formatRecorder{body: []byte("page")}
	resolver := NewMutationResolver(nil, rdb, nil)
	resolver.billingChecker = func(context.Context, string, oneapi.Price, string) error { return nil }
	resolver.fetcher = recorder.fetch

	captured := map[string]any{}
	resolver.auditHook = func(parameters map[string]any) {
		for key, value := range parameters {
			captured[key] = value
		}
	}

	_, err := resolver.WebFetch(contractContext(t), "https://8.8.8.8/", boolPtr(false))
	require.NoError(t, err)
	require.Equal(t, false, captured["output_markdown"])
	require.Equal(t, "https://8.8.8.8", captured["url"], "the audit copy keeps only scheme and host")
}

func boolPtr(value bool) *bool { return &value }
