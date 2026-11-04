package log

import (
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"

	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
)

// webFetchContextFields authors operation context for the default production
// diagnostic hook. Legacy error objects and explicit-filter adapters are unchanged.
func webFetchContextFields(summary WebFetchFailureSummary, fields []zapcore.Field) []zapcore.Field {
	var context []zapcore.Field
	if summary.TargetScheme != "" && summary.TargetHostname != "" {
		context = append(context, zap.String("target_scheme", summary.TargetScheme), zap.String("target_hostname", summary.TargetHostname))
	}
	if summary.ErrorCode != webFetchErrorCrawler || summary.Stage != "render" {
		return context
	}
	context = append(context, zap.String("caller_stage", summary.Stage), zap.String("caller_phase", "worker_result"))
	stage, _ := crawleregress.WorkerFailureDiagnostic(summary.WorkerStage, summary.WorkerCategory)
	if stage == "" {
		return context
	}
	for i := range fields {
		if fields[i].Key == "stage" {
			fields[i] = zap.String("stage", stage)
		}
	}
	context = append(context, zap.String("effective_worker_stage", stage))
	value := crawleregress.ReviewedFailureContext(summary.WorkerContext)
	if value == nil {
		return context
	}
	for _, field := range []struct{ name, value string }{
		{"final_scheme", value.FinalScheme}, {"final_hostname", value.FinalHostname}, {"content_type", value.ContentType}, {"converter", value.Converter},
	} {
		if field.value != "" {
			context = append(context, zap.String(field.name, field.value))
		}
	}
	for _, field := range []struct {
		name  string
		value *int64
	}{
		{"upstream_status", value.UpstreamStatus}, {"redirect_count", value.RedirectCount}, {"rendered_html_bytes", value.RenderedHTMLBytes},
		{"conversion_input_bytes", value.ConversionInputBytes}, {"conversion_output_bytes", value.ConversionOutputBytes},
		{"conversion_output_trimmed_bytes", value.ConversionOutputTrimmedBytes}, {"body_text_runes", value.BodyTextRunes}, {"body_text_nodes", value.BodyTextNodes},
	} {
		if field.value != nil {
			context = append(context, zap.Int64(field.name, *field.value))
		}
	}
	return context
}
