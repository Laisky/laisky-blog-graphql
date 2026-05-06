package pageindex

import (
	"bytes"
	"fmt"
	"sort"
	"sync"
	"text/template"

	errors "github.com/Laisky/errors/v2"
)

// Prompt names are the identifiers used by Render and the golden corpus.
const (
	PromptCheckTitleAppearance        = "check_title_appearance"
	PromptCheckTitleAppearanceInStart = "check_title_appearance_in_start"
	PromptTOCDetectorSinglePage       = "toc_detector_single_page"
	PromptDetectPageIndex             = "detect_page_index"
	PromptTOCTransformer              = "toc_transformer"
	PromptTOCIndexExtractor           = "toc_index_extractor"
	PromptAddPageNumberToTOC          = "add_page_number_to_toc"
	PromptGenerateTOCInit             = "generate_toc_init"
	PromptGenerateTOCContinue         = "generate_toc_continue"
	PromptSingleTOCItemIndexFixer     = "single_toc_item_index_fixer"
	PromptGenerateNodeSummary         = "generate_node_summary"
	PromptGenerateDocDescription      = "generate_doc_description"
	PromptPickPageRanges              = "pick_page_ranges"
)

// CheckTitleAppearanceVars renders PromptCheckTitleAppearance.
type CheckTitleAppearanceVars struct {
	Title    string
	PageText string
}

// CheckTitleAppearanceInStartVars renders PromptCheckTitleAppearanceInStart.
type CheckTitleAppearanceInStartVars struct {
	Title    string
	PageText string
}

// TOCDetectorSinglePageVars renders PromptTOCDetectorSinglePage.
type TOCDetectorSinglePageVars struct {
	Content string
}

// DetectPageIndexVars renders PromptDetectPageIndex.
type DetectPageIndexVars struct {
	TOCContent string
}

// TOCTransformerVars renders PromptTOCTransformer.
type TOCTransformerVars struct {
	TOCContent string
}

// TOCIndexExtractorVars renders PromptTOCIndexExtractor.
type TOCIndexExtractorVars struct {
	TOC     string
	Content string
}

// AddPageNumberToTOCVars renders PromptAddPageNumberToTOC.
type AddPageNumberToTOCVars struct {
	Part      string
	Structure string
}

// GenerateTOCInitVars renders PromptGenerateTOCInit.
type GenerateTOCInitVars struct {
	Part string
}

// GenerateTOCContinueVars renders PromptGenerateTOCContinue.
type GenerateTOCContinueVars struct {
	Part           string
	PreviousStruct string
}

// SingleTOCItemIndexFixerVars renders PromptSingleTOCItemIndexFixer.
type SingleTOCItemIndexFixerVars struct {
	SectionTitle string
	Content      string
}

// GenerateNodeSummaryVars renders PromptGenerateNodeSummary.
type GenerateNodeSummaryVars struct {
	Text string
}

// GenerateDocDescriptionVars renders PromptGenerateDocDescription.
type GenerateDocDescriptionVars struct {
	Structure string
}

// PickPageRangesVars renders PromptPickPageRanges (retrieval mini-agent).
type PickPageRangesVars struct {
	Query     string
	Tree      string
	MaxRanges int
}

// Source: pageindex/page_index.py:23-37 (check_title_appearance prompt body).
const promptCheckTitleAppearance = `
    Your job is to check if the given section appears or starts in the given page_text.

    Note: do fuzzy matching, ignore any space inconsistency in the page_text.

    The given section title is {{.Title}}.
    The given page_text is {{.PageText}}.

    Reply format:
    {

        "thinking": <why do you think the section appears or starts in the page_text>
        "answer": "yes or no" (yes if the section appears or starts in the page_text, no otherwise)
    }
    Directly return the final JSON structure. Do not output anything else.`

// Source: pageindex/page_index.py:49-65 (check_title_appearance_in_start prompt body).
const promptCheckTitleAppearanceInStart = `
    You will be given the current section title and the current page_text.
    Your job is to check if the current section starts in the beginning of the given page_text.
    If there are other contents before the current section title, then the current section does not start in the beginning of the given page_text.
    If the current section title is the first content in the given page_text, then the current section starts in the beginning of the given page_text.

    Note: do fuzzy matching, ignore any space inconsistency in the page_text.

    The given section title is {{.Title}}.
    The given page_text is {{.PageText}}.

    reply format:
    {
        "thinking": <why do you think the section appears or starts in the page_text>
        "start_begin": "yes or no" (yes if the section starts in the beginning of the page_text, no otherwise)
    }
    Directly return the final JSON structure. Do not output anything else.`

// Source: pageindex/page_index.py:105-117 (toc_detector_single_page prompt body).
const promptTOCDetectorSinglePage = `
    Your job is to detect if there is a table of content provided in the given text.

    Given text: {{.Content}}

    return the following JSON format:
    {
        "thinking": <why do you think there is a table of content in the given text>
        "toc_detected": "<yes or no>",
    }

    Directly return the final JSON structure. Do not output anything else.
    Please note: abstract,summary, notation list, figure list, table list, etc. are not table of contents.`

// Source: pageindex/page_index.py:204-216 (detect_page_index prompt body).
const promptDetectPageIndex = `
    You will be given a table of contents.

    Your job is to detect if there are page numbers/indices given within the table of contents.

    Given text: {{.TOCContent}}

    Reply format:
    {
        "thinking": <why do you think there are page numbers/indices given within the table of contents>
        "page_index_given_in_toc": "<yes or no>"
    }
    Directly return the final JSON structure. Do not output anything else.`

// Source: pageindex/page_index.py:275-292 (toc_transformer init prompt body) +
// the table_of_contents suffix added inline.
const promptTOCTransformer = `
    You are given a table of contents, You job is to transform the whole table of content into a JSON format included table_of_contents.

    structure is the numeric system which represents the index of the hierarchy section in the table of contents. For example, the first section has structure index 1, the first subsection has structure index 1.1, the second subsection has structure index 1.2, etc.

    The response should be in the following JSON format:
    {
    table_of_contents: [
        {
            "structure": <structure index, "x.x.x" or None> (string),
            "title": <title of the section>,
            "page": <page number or None>,
        },
        ...
        ],
    }
    You should transform the full table of contents in one go.
    Directly return the final JSON structure, do not output anything else.
 Given table of contents
:{{.TOCContent}}`

// Source: pageindex/page_index.py:245-264 (toc_index_extractor prompt body).
const promptTOCIndexExtractor = `
    You are given a table of contents in a json format and several pages of a document, your job is to add the physical_index to the table of contents in the json format.

    The provided pages contains tags like <physical_index_X> and <physical_index_X> to indicate the physical location of the page X.

    The structure variable is the numeric system which represents the index of the hierarchy section in the table of contents. For example, the first section has structure index 1, the first subsection has structure index 1.1, the second subsection has structure index 1.2, etc.

    The response should be in the following JSON format:
    [
        {
            "structure": <structure index, "x.x.x" or None> (string),
            "title": <title of the section>,
            "physical_index": "<physical_index_X>" (keep the format)
        },
        ...
    ]

    Only add the physical_index to the sections that are in the provided pages.
    If the section is not in the provided pages, do not add the physical_index to it.
    Directly return the final JSON structure. Do not output anything else.
Table of contents:
{{.TOC}}
Document pages:
{{.Content}}`

// Source: pageindex/page_index.py:462-482 (add_page_number_to_toc fill prompt body).
const promptAddPageNumberToTOC = `
    You are given an JSON structure of a document and a partial part of the document. Your task is to check if the title that is described in the structure is started in the partial given document.

    The provided text contains tags like <physical_index_X> and <physical_index_X> to indicate the physical location of the page X.

    If the full target section starts in the partial given document, insert the given JSON structure with the "start": "yes", and "start_index": "<physical_index_X>".

    If the full target section does not start in the partial given document, insert "start": "no",  "start_index": None.

    The response should be in the following format.
        [
            {
                "structure": <structure index, "x.x.x" or None> (string),
                "title": <title of the section>,
                "start": "<yes or no>",
                "physical_index": "<physical_index_X> (keep the format)" or None
            },
            ...
        ]
    The given structure contains the result of the previous part, you need to fill the result of the current part, do not change the previous result.
    Directly return the final JSON structure. Do not output anything else.

Current Partial Document:
{{.Part}}

Given Structure
{{.Structure}}
`

// Source: pageindex/page_index.py:544-566 (generate_toc_init prompt body).
const promptGenerateTOCInit = `
    You are an expert in extracting hierarchical tree structure, your task is to generate the tree structure of the document.

    The structure variable is the numeric system which represents the index of the hierarchy section in the table of contents. For example, the first section has structure index 1, the first subsection has structure index 1.1, the second subsection has structure index 1.2, etc.

    For the title, you need to extract the original title from the text, only fix the space inconsistency.

    The provided text contains tags like <physical_index_X> and <physical_index_X> to indicate the start and end of page X.

    For the physical_index, you need to extract the physical index of the start of the section from the text. Keep the <physical_index_X> format.

    The response should be in the following format.
        [
            {
                "structure": <structure index, "x.x.x"> (string),
                "title": <title of the section, keep the original title>,
                "physical_index": "<physical_index_X> (keep the format)"
            },

        ],


    Directly return the final JSON structure. Do not output anything else.
Given text
:{{.Part}}`

// Source: pageindex/page_index.py:509-532 (generate_toc_continue prompt body).
const promptGenerateTOCContinue = `
    You are an expert in extracting hierarchical tree structure.
    You are given a tree structure of the previous part and the text of the current part.
    Your task is to continue the tree structure from the previous part to include the current part.

    The structure variable is the numeric system which represents the index of the hierarchy section in the table of contents. For example, the first section has structure index 1, the first subsection has structure index 1.1, the second subsection has structure index 1.2, etc.

    For the title, you need to extract the original title from the text, only fix the space inconsistency.

    The provided text contains tags like <physical_index_X> and <physical_index_X> to indicate the start and end of page X.

    For the physical_index, you need to extract the physical index of the start of the section from the text. Keep the <physical_index_X> format.

    The response should be in the following format.
        [
            {
                "structure": <structure index, "x.x.x"> (string),
                "title": <title of the section, keep the original title>,
                "physical_index": "<physical_index_X> (keep the format)"
            },
            ...
        ]

    Directly return the additional part of the final JSON structure. Do not output anything else.
Given text
:{{.Part}}
Previous tree structure
:{{.PreviousStruct}}`

// Source: pageindex/page_index.py:741-751 (single_toc_item_index_fixer prompt body).
const promptSingleTOCItemIndexFixer = `
    You are given a section title and several pages of a document, your job is to find the physical index of the start page of the section in the partial document.

    The provided pages contains tags like <physical_index_X> and <physical_index_X> to indicate the physical location of the page X.

    Reply in a JSON format:
    {
        "thinking": <explain which page, started and closed by <physical_index_X>, contains the start of this section>,
        "physical_index": "<physical_index_X>" (keep the format)
    }
    Directly return the final JSON structure. Do not output anything else.
Section Title:
{{.SectionTitle}}
Document pages:
{{.Content}}`

// Source: pageindex/utils.py:579-585 (generate_node_summary prompt body).
const promptGenerateNodeSummary = `You are given a part of a document, your task is to generate a description of the partial document about what are main points covered in the partial document.

    Partial Document Text: {{.Text}}

    Directly return the description, do not include any other text.
    `

// Source: pageindex/utils.py:622-629 (generate_doc_description prompt body).
const promptGenerateDocDescription = `Your are an expert in generating descriptions for a document.
    You are given a structure of a document. Your task is to generate a one-sentence description for the document, which makes it easy to distinguish the document from other documents.

    Document Structure: {{.Structure}}

    Directly return the description, do not include any other text.
    `

// Source: pageindex/retrieve.py — derived from the get_document_structure +
// get_page_content tool contract (the upstream agent loop is implemented in the
// example notebooks rather than as a single prompt). The retrieval prompt below
// matches the body the upstream README documents for the "pick page ranges"
// step in §2.6.2 / retrieve.py:79-137.
const promptPickPageRanges = `
    You are a document-retrieval planner. You are given a query and a JSON tree of a single document.
    Your job is to pick up to {{.MaxRanges}} page ranges from the document that are most likely to contain the answer to the query.

    Each range MUST be expressed as inclusive integer page numbers from the tree's start_index/end_index fields.

    Query: {{.Query}}

    Document tree (text fields removed for brevity):
    {{.Tree}}

    Reply in JSON of the following shape:
    {
        "ranges": [
            {"start": <int>, "end": <int>, "reason": <one-sentence reason>}
        ]
    }
    Return the JSON only, no surrounding text.`

var (
	promptOnce sync.Once
	prompts    map[string]*template.Template
)

func loadPrompts() map[string]*template.Template {
	promptOnce.Do(func() {
		prompts = map[string]*template.Template{
			PromptCheckTitleAppearance:        template.Must(template.New(PromptCheckTitleAppearance).Parse(promptCheckTitleAppearance)),
			PromptCheckTitleAppearanceInStart: template.Must(template.New(PromptCheckTitleAppearanceInStart).Parse(promptCheckTitleAppearanceInStart)),
			PromptTOCDetectorSinglePage:       template.Must(template.New(PromptTOCDetectorSinglePage).Parse(promptTOCDetectorSinglePage)),
			PromptDetectPageIndex:             template.Must(template.New(PromptDetectPageIndex).Parse(promptDetectPageIndex)),
			PromptTOCTransformer:              template.Must(template.New(PromptTOCTransformer).Parse(promptTOCTransformer)),
			PromptTOCIndexExtractor:           template.Must(template.New(PromptTOCIndexExtractor).Parse(promptTOCIndexExtractor)),
			PromptAddPageNumberToTOC:          template.Must(template.New(PromptAddPageNumberToTOC).Parse(promptAddPageNumberToTOC)),
			PromptGenerateTOCInit:             template.Must(template.New(PromptGenerateTOCInit).Parse(promptGenerateTOCInit)),
			PromptGenerateTOCContinue:         template.Must(template.New(PromptGenerateTOCContinue).Parse(promptGenerateTOCContinue)),
			PromptSingleTOCItemIndexFixer:     template.Must(template.New(PromptSingleTOCItemIndexFixer).Parse(promptSingleTOCItemIndexFixer)),
			PromptGenerateNodeSummary:         template.Must(template.New(PromptGenerateNodeSummary).Parse(promptGenerateNodeSummary)),
			PromptGenerateDocDescription:      template.Must(template.New(PromptGenerateDocDescription).Parse(promptGenerateDocDescription)),
			PromptPickPageRanges:              template.Must(template.New(PromptPickPageRanges).Parse(promptPickPageRanges)),
		}
	})
	return prompts
}

// RenderPrompt renders the named template with data and returns the body string.
func RenderPrompt(name string, data any) (string, error) {
	tpl, ok := loadPrompts()[name]
	if !ok {
		return "", errors.Errorf("unknown prompt %q", name)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", errors.Wrapf(err, "render prompt %q", name)
	}
	return buf.String(), nil
}

// PromptNames returns the sorted list of registered prompt names.
func PromptNames() []string {
	out := make([]string, 0, len(loadPrompts()))
	for k := range prompts {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// EnsurePromptsLoaded panics on misconfiguration; tests call it for fast failure.
func EnsurePromptsLoaded() {
	loadPrompts()
	if got := len(prompts); got != 13 {
		panic(fmt.Sprintf("pageindex prompts: expected 13 templates, got %d", got))
	}
}
