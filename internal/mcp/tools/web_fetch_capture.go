package tools

import (
	"strings"
	"unicode/utf8"

	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	mcp "github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/library/buildidentity"
	"github.com/Laisky/laisky-blog-graphql/library/search"
)

const webFetchCaptureService = "laisky-blog-graphql"

// webFetchCapture keeps only declared input in memory until an ordinary failure
// log event. It never serializes the request envelope, headers or provider key.
type webFetchCapture struct {
	originalURL            string
	submittedURL           string
	outputMarkdown         bool
	outputMarkdownDeclared bool
	declaredOutputMarkdown *bool
	declaredOutputType     string
	attempted              bool
}

// newWebFetchCapture preserves input before normalization. The operation logger
// already owns request_id, so this helper does not add a duplicate context key.
func newWebFetchCapture(req mcp.CallToolRequest, originalURL, _ string) *webFetchCapture {
	capture := &webFetchCapture{
		originalURL: originalURL, submittedURL: strings.TrimSpace(originalURL),
		outputMarkdown: true, declaredOutputType: "absent",
	}
	value, present := req.GetArguments()["output_markdown"]
	capture.outputMarkdownDeclared = present
	if present {
		switch value := value.(type) {
		case nil:
			capture.declaredOutputType = "null"
		case bool:
			capture.declaredOutputType = "bool"
			capture.declaredOutputMarkdown = &value
			capture.outputMarkdown = value
		default:
			// Handle rejects other types before this constructor. Never traverse
			// an injected unsupported object or invoke an arbitrary serializer.
			capture.declaredOutputType = "unsupported"
		}
	}
	return capture
}

// writeFailure selects ordinary log fields once; it performs no storage or
// delivery operation and never changes the original crawl outcome.
func (capture *webFetchCapture) writeFailure(diagnostic search.FetchFailureDiagnostic, details ...zapcore.Field) []zapcore.Field {
	capture.attempted = true
	source, err := buildidentity.ProcessSource(webFetchCaptureService)
	fields := captureSourceFields(source)
	if err != nil {
		fields = append(fields, zap.NamedError("origin_identity_error", err))
	}
	for _, field := range []zapcore.Field{
		zap.String("error_code", diagnostic.ErrorCode), zap.String("stage", diagnostic.Stage),
	} {
		if !captureHasField(details, field.Key) {
			fields = append(fields, field)
		}
	}
	if !captureHasField(details, "caller_phase") {
		phase := diagnostic.Stage
		if diagnostic.ErrorCode == "crawler_failed" {
			phase = "worker_result"
		}
		fields = append(fields, zap.String("caller_phase", phase))
	}
	fields = append(fields, details...)
	fields = append(fields, zap.Bool("output_markdown_declared", capture.outputMarkdownDeclared),
		zap.String("declared_output_type", capture.declaredOutputType),
		zap.Any("declared_output_markdown", capture.declaredOutputMarkdown),
		zap.Bool("output_markdown", capture.outputMarkdown))
	fields = append(fields, captureStringFields("original_url", capture.originalURL)...)
	return append(fields, captureStringFields("submitted_url", capture.submittedURL)...)
}

// captureSourceFields selects only public source/process identity, without reading configuration.
func captureSourceFields(source buildidentity.Source) []zapcore.Field {
	fields := []zapcore.Field{zap.String("origin_service", webFetchCaptureService)}
	for _, field := range []struct{ key, value string }{
		{"origin_hostname", source.Hostname}, {"origin_process_run_id", source.ProcessRunID},
		{"origin_revision", source.Revision}, {"origin_go_version", source.GoVersion},
		{"origin_module_version", source.ModuleVersion},
	} {
		if field.value != "" {
			fields = append(fields, zap.String(field.key, field.value))
		}
	}
	if source.Revision != "" {
		fields = append(fields, zap.Bool("origin_modified", source.Modified))
	}
	return fields
}

// captureStringFields preserves valid text directly and uses Zap's standard
// binary field for invalid UTF-8 instead of silently repairing its bytes.
func captureStringFields(key, value string) []zapcore.Field {
	fields := []zapcore.Field{zap.Int(key+"_byte_length", len(value))}
	if utf8.ValidString(value) {
		return append(fields, zap.String(key+"_encoding", "utf8"), zap.String(key, value))
	}
	return append(fields, zap.String(key+"_encoding", "binary"), zap.Binary(key, []byte(value)))
}

// captureHasField avoids duplicate diagnostic keys while retaining existing authored context.
func captureHasField(fields []zapcore.Field, key string) bool {
	for _, field := range fields {
		if field.Key == key {
			return true
		}
	}
	return false
}
