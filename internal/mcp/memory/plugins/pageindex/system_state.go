package pageindex

import (
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// pageIndexSummaryInput builds the user-row summary publication. A deterministic
// fallback, or a tree whose LLM work was cut short (degradedReason non-empty), is
// published as degraded with the reason as its safe error code (§4.6).
func pageIndexSummaryInput(summary string, source files.SummarySource, contentHash, degradedReason string) files.PluginSummaryInput {
	status := files.SummaryStatusReady
	if source == files.SummarySourceDeterministicFallback || degradedReason != "" {
		status = files.SummaryStatusDegraded
	}
	return files.PluginSummaryInput{
		ExpectedContentHash: contentHash,
		Summary:             summary,
		WordCount:           files.SummaryWordCount(summary),
		Source:              source,
		Status:              status,
		PromptVersion:       AlgorithmVersion,
		GenerationKey:       contentHash,
		ErrorCode:           degradedReason,
	}
}
