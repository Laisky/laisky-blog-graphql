package pageindex

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update-golden", false, "regenerate prompt golden fixtures")

func TestPromptCount(t *testing.T) {
	EnsurePromptsLoaded()
	if got := len(PromptNames()); got != 13 {
		t.Fatalf("expected 13 prompts, got %d", got)
	}
}

func TestPromptsGolden(t *testing.T) {
	cases := []struct {
		name string
		data any
	}{
		{PromptCheckTitleAppearance, CheckTitleAppearanceVars{Title: "Chapter 1", PageText: "Page text body"}},
		{PromptCheckTitleAppearanceInStart, CheckTitleAppearanceInStartVars{Title: "Section A", PageText: "Top of page"}},
		{PromptTOCDetectorSinglePage, TOCDetectorSinglePageVars{Content: "Table of Contents\nIntro 1"}},
		{PromptDetectPageIndex, DetectPageIndexVars{TOCContent: "Intro 1\nMethods 5"}},
		{PromptTOCTransformer, TOCTransformerVars{TOCContent: "1 Intro\n2 Methods"}},
		{PromptTOCIndexExtractor, TOCIndexExtractorVars{TOC: "[]", Content: "<physical_index_1>...<physical_index_1>"}},
		{PromptAddPageNumberToTOC, AddPageNumberToTOCVars{Part: "<physical_index_1>...", Structure: "[]"}},
		{PromptGenerateTOCInit, GenerateTOCInitVars{Part: "<physical_index_1>page text"}},
		{PromptGenerateTOCContinue, GenerateTOCContinueVars{Part: "<physical_index_2>page two", PreviousStruct: "[]"}},
		{PromptSingleTOCItemIndexFixer, SingleTOCItemIndexFixerVars{SectionTitle: "Methods", Content: "<physical_index_5>methods<physical_index_5>"}},
		{PromptGenerateNodeSummary, GenerateNodeSummaryVars{Text: "Sample text body"}},
		{PromptGenerateDocDescription, GenerateDocDescriptionVars{Structure: "[]"}},
		{PromptPickPageRanges, PickPageRangesVars{Query: "What is the conclusion?", Tree: "[]", MaxRanges: 5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderPrompt(tc.name, tc.data)
			if err != nil {
				t.Fatalf("render %s: %v", tc.name, err)
			}
			path := filepath.Join("testdata", "golden_prompts", tc.name+".txt")
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
					t.Logf("captured golden for %s", tc.name)
					return
				}
				t.Fatal(err)
			}
			if string(want) != got {
				t.Fatalf("golden mismatch for %s:\nwant:\n%s\n--- got ---\n%s", tc.name, want, got)
			}
		})
	}
}
