package tools

import (
	"strings"

	"github.com/Laisky/errors/v2"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	mcp "github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/library/crawlercapture"
	"github.com/Laisky/laisky-blog-graphql/library/search"
)

const webFetchCaptureService = "laisky-blog-graphql"

// webFetchCapture retains declared crawl input only. It never serializes the
// request envelope, headers, context, provider key or unknown tool arguments.
type webFetchCapture struct {
	record         crawlercapture.Record
	preparationErr error
	attempted      bool
}

// newWebFetchCapture snapshots only the declared inputs without normalizing their bytes.
func newWebFetchCapture(req mcp.CallToolRequest, originalURL, requestID string) *webFetchCapture {
	request := crawlercapture.Request{
		OriginalURL:            crawlercapture.CapturedString(originalURL),
		SubmittedURL:           crawlercapture.CapturedString(strings.TrimSpace(originalURL)),
		OutputMarkdown:         true,
		OriginalOutputMarkdown: crawlercapture.NotReached(),
	}
	value, present := req.GetArguments()["output_markdown"]
	request.OutputMarkdownDeclared = present
	var preparationErr error
	if present {
		// This constructor runs only after OptionalBool accepted a boolean or null.
		// Encode that declared value directly without traversing unknown arguments
		// or invoking arbitrary serializers from an injected request object.
		raw := []byte("null")
		if declared, ok := value.(bool); ok {
			request.DeclaredOutputMarkdown = &declared
			request.OutputMarkdown = declared
			if declared {
				raw = []byte("true")
			} else {
				raw = []byte("false")
			}
		} else if value != nil {
			preparationErr = errors.New("declared output option was not resolved")
		}
		request.OriginalOutputMarkdown = crawlercapture.Captured(raw)
	}

	return &webFetchCapture{record: crawlercapture.Record{
		Correlation: crawlercapture.Correlation{RequestID: requestID},
		Request:     request,
		Converter:   crawlercapture.Converter{Input: crawlercapture.NotReached()},
	}, preparationErr: preparationErr}
}

// writeFailure saves once before emitting a reference and preserves the original operation outcome.
func (capture *webFetchCapture) writeFailure(diagnostic search.FetchFailureDiagnostic, details ...zapcore.Field) []zapcore.Field {
	capture.attempted = true
	capture.record.Correlation.TaskID = diagnostic.TaskID
	capture.record.Failure = crawlercapture.Failure{Stage: diagnostic.Stage, Code: diagnostic.ErrorCode}
	if diagnostic.ErrorCode == "crawler_failed" {
		capture.record.Failure.Stage = "worker_result"
		if diagnostic.TaskID != "" {
			capture.record.Failure.WorkerStage = diagnostic.WorkerStage
			capture.record.Failure.WorkerCode = diagnostic.WorkerCategory
		}
	}
	if capture.preparationErr != nil {
		return append(captureInitializationFailure("input_encoding_failed", capture.preparationErr), details...)
	}
	journal, err := crawlercapture.JournalFromEnvironment(webFetchCaptureService)
	if err != nil {
		return append(captureInitializationFailure("write_failed", err), details...)
	}
	reference, err := journal.Write(capture.record)
	reference.Source = journal.Source()
	return captureReferenceFields(reference, err, details...)
}

// captureSourceFields reports the same emitter identity as the private record.
func captureSourceFields(source crawlercapture.Source) []zapcore.Field {
	return []zapcore.Field{
		zap.String("origin_service", source.Service),
		zap.String("origin_hostname", source.Hostname),
		zap.String("origin_process_run_id", source.ProcessRunID),
	}
}

// captureInitializationFailure makes an unavailable capture explicit without
// pretending that an incomplete or unwritten record has a retrievable digest.
func captureInitializationFailure(status string, err error) []zapcore.Field {
	source, sourceErr := crawlercapture.ProcessSource(webFetchCaptureService)
	fields := captureSourceFields(source)
	fields = append(fields, zap.String("failure_capture_status", status), zap.NamedError("failure_capture_error", err))
	if sourceErr != nil {
		fields = append(fields, zap.NamedError("failure_capture_source_error", sourceErr))
	}
	return fields
}

// captureReferenceFields puts bounded recovery identity before details and the
// complete path last; a downstream notification preview cannot hide the digest.
func captureReferenceFields(reference crawlercapture.Reference, err error, details ...zapcore.Field) []zapcore.Field {
	fields := captureSourceFields(reference.Source)
	fields = append(fields, zap.String("failure_capture_status", reference.Status),
		zap.String("failure_capture_id", reference.CaptureID))
	if reference.SHA256 != "" {
		fields = append(fields, zap.String("failure_capture_sha256", reference.SHA256),
			zap.Int64("failure_capture_bytes", reference.Bytes))
	}
	fields = append(fields, details...)
	if err != nil {
		fields = append(fields, zap.NamedError("failure_capture_error", err))
	}
	if reference.Path != "" {
		fields = append(fields, zap.String("failure_capture_path", reference.Path))
	}
	return fields
}
