package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	mcp "github.com/mark3labs/mcp-go/mcp"
	"golang.org/x/sync/errgroup"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
)

// PipeInvoker executes a named MCP tool using the provided arguments.
// It returns the tool result in MCP wire shape or an error for exceptional conditions.
type PipeInvoker func(context.Context, string, any) (*mcp.CallToolResult, error)

// PipeLimits defines safety limits for mcp_pipe execution.
type PipeLimits struct {
	MaxSteps    int
	MaxDepth    int
	MaxParallel int
}

// MCPPipeTool implements the mcp_pipe MCP tool.
//
// It executes a declarative pipeline containing sequential steps, parallel groups,
// and nested pipelines, feeding outputs forward through variable interpolation.
type MCPPipeTool struct {
	logger  logSDK.Logger
	invoker PipeInvoker
	limits  PipeLimits
}

// NewMCPPipeTool constructs an MCPPipeTool.
func NewMCPPipeTool(logger logSDK.Logger, invoker PipeInvoker, limits PipeLimits) (*MCPPipeTool, error) {
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	if invoker == nil {
		return nil, errors.New("invoker is required")
	}
	if limits.MaxSteps <= 0 {
		limits.MaxSteps = 50
	}
	if limits.MaxDepth <= 0 {
		limits.MaxDepth = 5
	}
	if limits.MaxParallel <= 0 {
		limits.MaxParallel = 8
	}

	return &MCPPipeTool{
		logger:  logger,
		invoker: invoker,
		limits:  limits,
	}, nil
}

// Definition returns the MCP metadata describing the tool.
func (t *MCPPipeTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"mcp_pipe",
		mcp.WithDescription("Execute a pipeline of MCP tools (sequential, parallel, nested) with output passing."),
		mcp.WithString(
			"spec",
			mcp.Description("Pipeline specification. Either a JSON object or a JSON-encoded string."),
		),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	)
}

// Handle executes the mcp_pipe pipeline.
func (t *MCPPipeTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Prefer a request-scoped logger when present in context.
	logger := t.logger
	if ctxLogger, ok := ctx.Value(ctxkeys.Logger).(logSDK.Logger); ok && ctxLogger != nil {
		logger = ctxLogger.Named("mcp_pipe")
	}

	spec, err := parsePipeSpec(req.Params.Arguments)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if len(spec.Steps) == 0 {
		return mcp.NewToolResultError("steps cannot be empty"), nil
	}

	env := map[string]any{
		"vars":  spec.Vars,
		"steps": map[string]any{},
	}

	counter := &stepCounter{}
	result, execErr := t.executeSpec(ctx, logger, spec, env, 0, counter)

	payload := map[string]any{
		"ok":     execErr == nil,
		"error":  errorString(execErr),
		"result": result,
		"steps":  env["steps"],
	}

	toolResult, encodeErr := mcp.NewToolResultJSON(payload)
	if encodeErr != nil {
		logger.Error("mcp_pipe encode response", zap.Error(encodeErr))
		return mcp.NewToolResultError("failed to encode mcp_pipe response"), nil
	}
	if execErr != nil {
		toolResult.IsError = true
	}

	return toolResult, nil
}

// pipeSpec defines the JSON schema accepted by mcp_pipe.
//
// It is intentionally small: clients can express sequential steps, parallel
// groups, nested pipelines, and a final return selector.
type pipeSpec struct {
	Vars            map[string]any `json:"vars,omitempty"`
	Steps           []pipeStep     `json:"steps"`
	Return          any            `json:"return,omitempty"`
	ContinueOnError bool           `json:"continue_on_error,omitempty"`
}

// pipeStep represents a single pipeline step.
type pipeStep struct {
	ID       string         `json:"id"`
	Tool     string         `json:"tool,omitempty"`
	Args     map[string]any `json:"args,omitempty"`
	Parallel []pipeStep     `json:"parallel,omitempty"`
	Pipe     *pipeSpec      `json:"pipe,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
	_        map[string]any `json:"-"`
}

// stepCounter tracks executed steps to enforce MaxSteps.
type stepCounter struct {
	mu    sync.Mutex
	count int
}

// parsePipeSpec parses the input arguments into a pipeSpec.
func parsePipeSpec(arguments any) (*pipeSpec, error) {
	args, ok := arguments.(map[string]any)
	if ok {
		if rawSpec, ok := args["spec"]; ok {
			return parsePipeSpecRaw(rawSpec)
		}
		// Allow passing the spec directly as the arguments object.
		return parsePipeSpecRaw(args)
	}

	// Some clients may send raw JSON (json.RawMessage).
	if raw, ok := arguments.(json.RawMessage); ok {
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, errors.Wrap(err, "decode spec")
		}
		return parsePipeSpecRaw(decoded)
	}

	return nil, errors.New("invalid arguments: expected object")
}

// parsePipeSpecRaw parses a spec that may be a JSON string or an object.
func parsePipeSpecRaw(raw any) (*pipeSpec, error) {
	switch v := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil, errors.New("spec cannot be empty")
		}
		var spec pipeSpec
		if err := json.Unmarshal([]byte(trimmed), &spec); err != nil {
			return nil, errors.Wrap(err, "decode spec")
		}
		return &spec, nil
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return nil, errors.Wrap(err, "encode spec")
		}
		var spec pipeSpec
		if err := json.Unmarshal(data, &spec); err != nil {
			return nil, errors.Wrap(err, "decode spec")
		}
		return &spec, nil
	}
}

// executeSpec executes a pipeline spec and returns the selected return value.
func (t *MCPPipeTool) executeSpec(ctx context.Context, logger logSDK.Logger, spec *pipeSpec, env map[string]any, depth int, counter *stepCounter) (any, error) {
	if depth > t.limits.MaxDepth {
		return nil, errors.New("pipeline nesting too deep")
	}

	stepsEnv, ok := env["steps"].(map[string]any)
	if !ok {
		return nil, errors.New("invalid steps environment")
	}

	seen := map[string]struct{}{}
	var pipelineErr error
	for i := range spec.Steps {
		step := spec.Steps[i]
		if err := validateStep(step); err != nil {
			return nil, err
		}
		if _, ok := seen[step.ID]; ok {
			return nil, errors.New("duplicate step id: " + step.ID)
		}
		seen[step.ID] = struct{}{}

		if err := counter.increment(t.limits.MaxSteps); err != nil {
			return nil, err
		}

		stepResult, err := t.executeStep(ctx, logger, step, env, depth, counter)
		stepsEnv[step.ID] = stepResult
		env["last"] = stepResult

		if err != nil {
			if pipelineErr == nil {
				pipelineErr = err
			}
			if !spec.ContinueOnError {
				return t.resolveReturn(spec, env), pipelineErr
			}
		}
	}

	return t.resolveReturn(spec, env), pipelineErr
}

// resolveReturn resolves the final return selector for a spec.
func (t *MCPPipeTool) resolveReturn(spec *pipeSpec, env map[string]any) any {
	if spec.Return == nil {
		return env["last"]
	}

	resolved, err := resolveAny(spec.Return, env)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return resolved
}

// executeStep runs a single step and returns a JSON-serializable result map.
func (t *MCPPipeTool) executeStep(ctx context.Context, logger logSDK.Logger, step pipeStep, env map[string]any, depth int, counter *stepCounter) (map[string]any, error) {
	startedAt := time.Now().UTC()

	if step.Tool != "" {
		resolvedArgs, err := resolveAny(step.Args, env)
		if err != nil {
			logger.Debug("mcp_pipe resolve args failed",
				zap.String("step_id", step.ID),
				zap.String("tool", step.Tool),
				zap.Strings("arg_keys", mapKeys(step.Args)),
				zap.Error(err),
			)
			return stepResultMap(step, startedAt, time.Since(startedAt), nil, err), err
		}

		result, invokeErr := t.invoker(ctx, step.Tool, resolvedArgs)
		dur := time.Since(startedAt)
		if invokeErr != nil {
			logger.Debug("mcp_pipe invoke tool failed",
				zap.String("step_id", step.ID),
				zap.String("tool", step.Tool),
				zap.Error(invokeErr),
			)
			return stepResultMap(step, startedAt, dur, result, invokeErr), invokeErr
		}
		if result != nil && result.IsError {
			err := errors.New(toolResultText(result))
			logger.Debug("mcp_pipe tool returned error",
				zap.String("step_id", step.ID),
				zap.String("tool", step.Tool),
				zap.String("tool_error", err.Error()),
			)
			return stepResultMap(step, startedAt, dur, result, err), err
		}
		return stepResultMap(step, startedAt, dur, result, nil), nil
	}

	if step.Pipe != nil {
		childEnv := map[string]any{
			"vars":  env["vars"],
			"steps": map[string]any{},
		}
		childResult, err := t.executeSpec(ctx, logger, step.Pipe, childEnv, depth+1, counter)
		dur := time.Since(startedAt)
		result := map[string]any{
			"id":          step.ID,
			"kind":        "pipe",
			"started_at":  startedAt.Format(time.RFC3339Nano),
			"duration_ms": dur.Milliseconds(),
			"ok":          err == nil,
			"error":       errorString(err),
			"result":      childResult,
			"steps":       childEnv["steps"],
		}
		return result, err
	}

	// parallel group
	children := step.Parallel
	childResults := map[string]any{}
	var mu sync.Mutex
	var groupErr error

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(t.limits.MaxParallel)

	seen := map[string]struct{}{}
	for i := range children {
		child := children[i]
		if err := validateStep(child); err != nil {
			return stepResultMap(step, startedAt, time.Since(startedAt), nil, err), err
		}
		if _, ok := seen[child.ID]; ok {
			err := errors.New("duplicate parallel step id: " + child.ID)
			return stepResultMap(step, startedAt, time.Since(startedAt), nil, err), err
		}
		seen[child.ID] = struct{}{}

		if err := counter.increment(t.limits.MaxSteps); err != nil {
			return stepResultMap(step, startedAt, time.Since(startedAt), nil, err), err
		}

		childCopy := child
		g.Go(func() error {
			res, err := t.executeStep(gctx, logger, childCopy, env, depth, counter)
			mu.Lock()
			childResults[childCopy.ID] = res
			if err != nil && groupErr == nil {
				groupErr = err
			}
			mu.Unlock()
			return nil
		})
	}

	_ = g.Wait()
	dur := time.Since(startedAt)

	result := map[string]any{
		"id":          step.ID,
		"kind":        "parallel",
		"started_at":  startedAt.Format(time.RFC3339Nano),
		"duration_ms": dur.Milliseconds(),
		"ok":          groupErr == nil,
		"error":       errorString(groupErr),
		"children":    childResults,
	}

	if groupErr != nil {
		return result, groupErr
	}

	return result, nil
}

// validateStep validates that the step contains exactly one execution mode.
func validateStep(step pipeStep) error {
	id := strings.TrimSpace(step.ID)
	if id == "" {
		return errors.New("step.id cannot be empty")
	}
	modeCount := 0
	if strings.TrimSpace(step.Tool) != "" {
		modeCount++
	}
	if step.Pipe != nil {
		modeCount++
	}
	if len(step.Parallel) > 0 {
		modeCount++
	}
	if modeCount != 1 {
		return errors.New("step must have exactly one of tool, pipe, or parallel")
	}
	return nil
}

// increment increases the step counter and enforces MaxSteps.
func (c *stepCounter) increment(max int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
	if c.count > max {
		return errors.New("pipeline exceeds maximum step limit")
	}
	return nil
}

// stepResultMap builds a normalized step result representation.
func stepResultMap(step pipeStep, startedAt time.Time, dur time.Duration, toolResult *mcp.CallToolResult, stepErr error) map[string]any {
	result := map[string]any{
		"id":          step.ID,
		"kind":        "tool",
		"tool":        step.Tool,
		"started_at":  startedAt.Format(time.RFC3339Nano),
		"duration_ms": dur.Milliseconds(),
		"ok":          stepErr == nil,
		"error":       errorString(stepErr),
	}

	if toolResult != nil {
		result["structured"] = normalizeJSONCompatible(toolResult.StructuredContent)
		result["text"] = toolResultText(toolResult)
	}

	return result
}

var placeholderPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// resolveAny resolves references and interpolations inside a value.
func resolveAny(value any, env map[string]any) (any, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case string:
		return resolveString(v, env)
	case map[string]any:
		if ref, ok := v["$ref"]; ok && len(v) == 1 {
			refStr, ok := ref.(string)
			if !ok {
				return nil, errors.New("$ref must be a string")
			}
			return resolvePath(refStr, env)
		}

		out := make(map[string]any, len(v))
		for key, val := range v {
			resolved, err := resolveAny(val, env)
			if err != nil {
				return nil, err
			}
			out[key] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			resolved, err := resolveAny(item, env)
			if err != nil {
				return nil, err
			}
			out = append(out, resolved)
		}
		return out, nil
	default:
		return v, nil
	}
}

// resolveString expands ${path} placeholders inside a string.
func resolveString(s string, env map[string]any) (string, error) {
	matches := placeholderPattern.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s, nil
	}

	var b strings.Builder
	cursor := 0
	for _, m := range matches {
		start := m[0]
		end := m[1]
		pathStart := m[2]
		pathEnd := m[3]

		b.WriteString(s[cursor:start])
		path := strings.TrimSpace(s[pathStart:pathEnd])
		val, err := resolvePath(path, env)
		if err != nil {
			return "", err
		}
		b.WriteString(valueToString(val))
		cursor = end
	}
	b.WriteString(s[cursor:])
	return b.String(), nil
}

// resolvePath resolves a dotted path against an environment map.
func resolvePath(path string, env map[string]any) (any, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, errors.New("empty reference path")
	}

	parts := strings.Split(trimmed, ".")
	var current any = env
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, errors.New("invalid reference path")
		}

		idx, idxErr := strconv.Atoi(part)
		if idxErr == nil {
			slice, ok := current.([]any)
			if !ok {
				return nil, errors.New("reference segment is not an array: " + part)
			}
			if idx < 0 || idx >= len(slice) {
				return nil, errors.New("reference index out of range: " + part)
			}
			current = slice[idx]
			continue
		}

		m, ok := current.(map[string]any)
		if !ok {
			return nil, errors.New("reference segment is not an object: " + part)
		}
		next, ok := m[part]
		if !ok {
			return nil, errors.New("reference not found: " + part)
		}
		current = next
	}
	return current, nil
}

// valueToString converts a value to a stable string representation for interpolation.
func valueToString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	default:
		encoded, err := json.Marshal(v)
		if err == nil {
			return string(encoded)
		}
		return fmt.Sprint(v)
	}
}

// toolResultText extracts a text representation from a tool result.
func toolResultText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}

	parts := make([]string, 0, len(result.Content))
	for _, c := range result.Content {
		switch v := c.(type) {
		case mcp.TextContent:
			parts = append(parts, v.Text)
		default:
			// Ignore non-text content.
		}
	}
	return strings.Join(parts, "\n")
}

// errorString converts an error to a string.
func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// normalizeJSONCompatible converts a value into a JSON-compatible representation.
//
// This is used so that $ref paths can reliably traverse tool outputs. Some tools
// return structured content as Go structs. While they serialize to JSON fine,
// reflection-based traversal is complex and error-prone. Instead, we normalize
// by marshaling to JSON and unmarshaling into generic Go values (map/slice).
//
// If normalization fails, the original value is returned.
func normalizeJSONCompatible(value any) any {
	if value == nil {
		return nil
	}

	// Fast path: already generic.
	switch value.(type) {
	case map[string]any, []any, string, float64, bool, int, int64, uint64:
		return value
	}

	data, err := json.Marshal(value)
	if err != nil {
		return value
	}

	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return value
	}
	return decoded
}

// mapKeys returns a sorted key list for debug logging.
func mapKeys(m map[string]any) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
