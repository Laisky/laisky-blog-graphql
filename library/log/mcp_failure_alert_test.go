package log

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	"github.com/google/uuid"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// forbiddenMCPMarshaler detects accidental evaluation of arbitrary alert payloads.
type forbiddenMCPMarshaler struct{}

// MarshalLogObject must never run at the MCP alert boundary.
func (forbiddenMCPMarshaler) MarshalLogObject(zapcore.ObjectEncoder) error {
	panic("unreviewed payload marshaler executed")
}

// encodeMCPFailure renders one diagnostic field as standard JSON for assertions.
func encodeMCPFailure(t *testing.T, field zapcore.Field) (map[string]any, string) {
	t.Helper()
	encoder := zapcore.NewJSONEncoder(zapcore.EncoderConfig{})
	field.AddTo(encoder)
	buffer, err := encoder.EncodeEntry(zapcore.Entry{}, nil)
	require.NoError(t, err)
	defer buffer.Free()
	decoded, ok := adoptionJSONFields(t, buffer.String())[mcpFailureFieldKey].(map[string]any)
	require.True(t, ok)
	return decoded, buffer.String()
}

// TestMCPFailureAlertBoundary rejects payloads, forged envelopes and scope lookalikes.
func TestMCPFailureAlertBoundary(t *testing.T) {
	valid := MCPFailureDiagnosticField(MCPFailureSummary{Method: mcpgo.MethodToolsCall, RequestID: 3, ErrorKind: MCPFailureToolNotFound, ErrorCode: -32602})
	for _, tc := range []struct {
		name, scope, message string
		fields               []zapcore.Field
		useful               bool
	}{
		{"authored", "graphql.laisky-graphql.mcp_hooks", "mcp request failed", []zapcore.Field{valid}, true},
		{"missing", "graphql.laisky-graphql.mcp_hooks", "mcp request failed", nil, false},
		{"forged string", "graphql.laisky-graphql.mcp_hooks", "mcp request failed", []zapcore.Field{zap.String("mcp_failure", "private-canary")}, false},
		{"forged object", "graphql.laisky-graphql.mcp_hooks", "mcp request failed", []zapcore.Field{zap.Object("mcp_failure", forbiddenMCPMarshaler{})}, false},
		{"duplicates", "graphql.laisky-graphql.mcp_hooks", "mcp request failed", []zapcore.Field{valid, valid}, false},
		{"scope lookalike", "graphql.laisky-graphql.mcp_hooks_private", "mcp request failed", []zapcore.Field{valid}, false},
		{"unscoped root", "graphql.mcp_hooks", "mcp request failed", []zapcore.Field{valid}, false},
		{"other event", "graphql.laisky-graphql.mcp_hooks", "unrelated event", []zapcore.Field{valid}, false},
		{"downgraded probe", "graphql.laisky-graphql.mcp_hooks", "mcp request failed (non-critical)", []zapcore.Field{valid}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, messages := alertReceiver(t)
			alert := adoptionAlert(t, endpoint)
			logger := zap.New(WebDiagnosticAlertCore(alert)).Named(tc.scope).
				With(zap.String("private_bound", "private-bound-canary"), zap.Namespace("private_namespace"))
			fields := append([]zapcore.Field{}, tc.fields...)
			fields = append(fields, zap.String("request", "private-request-canary"), zap.Object("payload", forbiddenMCPMarshaler{}))
			logger.Error(tc.message, fields...)
			message := receivedAlert(t, messages)
			decoded := adoptionJSONFields(t, message)
			if tc.useful {
				require.Len(t, decoded, 1)
				require.Contains(t, decoded, "mcp_failure")
			} else {
				require.Empty(t, decoded)
			}
			require.NotContains(t, message, "canary")
			var projected []zapcore.Field
			hook := WebDiagnosticAlertHook(func(_ zapcore.Entry, values []zapcore.Field) error {
				projected = values
				return nil
			})
			require.NoError(t, hook(zapcore.Entry{LoggerName: tc.scope, Message: tc.message}, fields))
			if tc.useful {
				require.Len(t, projected, 1)
				require.Equal(t, "mcp_failure", projected[0].Key)
			} else {
				require.Empty(t, projected)
			}
			require.Empty(t, alertsBefore(t, alert, messages), "one source entry must enqueue exactly one alert")
		})
	}
}

// TestMCPFailureBoundaryContractNames pins the exported names producers use to the literal production scope.
func TestMCPFailureBoundaryContractNames(t *testing.T) {
	require.Equal(t, "graphql.laisky-graphql.mcp_hooks", mcpHooksLoggerName)
	require.Equal(t, "mcp request failed", MCPRequestFailedMessage)
	core, observed := observer.New(zapcore.DebugLevel)
	zap.New(core).Named(RootLoggerName).Named(ServiceLoggerScope).Named(MCPHooksLoggerScope).Error(MCPRequestFailedMessage)
	entries := observed.AllUntimed()
	require.Len(t, entries, 1)
	valid := MCPFailureDiagnosticField(MCPFailureSummary{Method: mcpgo.MethodToolsCall})
	require.Len(t, mcpFailureAlertFields(entries[0].Entry, []zapcore.Field{valid}), 1, "zap-composed producer names must reach the boundary")
}

// TestMCPFailureDiagnosticNormalization preserves safe IDs without encoding arbitrary client values.
func TestMCPFailureDiagnosticNormalization(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   any
		want any
	}{
		{"integer", 3, json.Number("3")},
		{"int64", int64(3), json.Number("3")},
		{"float", float64(3), json.Number("3")},
		{"negative", float64(-7), json.Number("-7")},
		{"json number", json.Number("3"), json.Number("3")},
		{"sdk int64", mcpgo.NewRequestId(int64(3)), json.Number("3")},
		{"sdk float", mcpgo.NewRequestId(float64(4)), json.Number("4")},
		{"sdk uuid", mcpgo.NewRequestId("0199c118-4229-7000-8000-000000000001"), "0199c118-4229-7000-8000-000000000001"},
		{"sdk private string", mcpgo.NewRequestId("private-id-canary"), nil},
		{"sdk nested", mcpgo.NewRequestId(mcpgo.NewRequestId(int64(3))), nil},
		{"uuid", "0199c118-4229-7000-8000-000000000001", "0199c118-4229-7000-8000-000000000001"},
		{"nil uuid", uuid.Nil.String(), nil},
		{"uppercase uuid", "0199C118-4229-7000-8000-000000000001", nil},
		{"private string", "private-id-canary", nil},
		{"object", forbiddenMCPMarshaler{}, nil},
		{"map", map[string]any{"private": "private-id-canary"}, nil},
		{"bool", true, nil},
		{"fraction", 1.25, nil},
		{"nan", math.NaN(), nil},
		{"infinite", math.Inf(1), nil},
		{"unsafe float", float64(1 << 60), nil},
		{"large", int64(1 << 60), nil},
		{"nil", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoded, raw := encodeMCPFailure(t, MCPFailureDiagnosticField(MCPFailureSummary{Method: "private-method-canary", RequestID: tc.id, ErrorKind: "private-error-canary", ErrorCode: 12345}))
			require.Equal(t, "unknown", decoded["method"])
			require.Equal(t, "request_failed", decoded["error_kind"])
			require.Equal(t, tc.want, decoded["request_id"])
			require.NotContains(t, decoded, "error_code")
			_, err := uuid.Parse(decoded["failure_id"].(string))
			require.NoError(t, err)
			require.NotContains(t, raw, "canary")
		})
	}
	for _, code := range []int{-32768, -32602, -32000} {
		decoded, _ := encodeMCPFailure(t, MCPFailureDiagnosticField(MCPFailureSummary{ErrorCode: code}))
		require.Equal(t, json.Number(strconv.Itoa(code)), decoded["error_code"])
	}
	for _, code := range []int{-32769, -31999, 0, 1, 404} {
		decoded, _ := encodeMCPFailure(t, MCPFailureDiagnosticField(MCPFailureSummary{ErrorCode: code}))
		require.NotContains(t, decoded, "error_code", code)
	}
	for kind := range mcpFailureKinds {
		decoded, _ := encodeMCPFailure(t, MCPFailureDiagnosticField(MCPFailureSummary{ErrorKind: kind}))
		require.Equal(t, string(kind), decoded["error_kind"])
	}
	for method := range mcpFailureMethods {
		decoded, _ := encodeMCPFailure(t, MCPFailureDiagnosticField(MCPFailureSummary{Method: method}))
		require.Equal(t, string(method), decoded["method"])
	}
	first, _ := encodeMCPFailure(t, MCPFailureDiagnosticField(MCPFailureSummary{}))
	second, _ := encodeMCPFailure(t, MCPFailureDiagnosticField(MCPFailureSummary{}))
	require.NotEqual(t, first["failure_id"], second["failure_id"], "every failure gets a fresh correlation ID")
}

// sdkPackageFiles parses the non-test sources of one installed SDK package.
func sdkPackageFiles(t *testing.T, fset *token.FileSet, importPath string) []*ast.File {
	t.Helper()
	pkg, err := build.Import(importPath, ".", build.FindOnly)
	require.NoError(t, err)
	entries, err := os.ReadDir(pkg.Dir)
	require.NoError(t, err)
	var files []*ast.File
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(pkg.Dir, entry.Name()), nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		files = append(files, file)
	}
	require.NotEmpty(t, files, importPath)
	return files
}

// isSelector reports whether expr is receiver.name for a plain identifier receiver.
func isSelector(expr ast.Expr, receiver, name string) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && ident.Name == receiver
}

// TestMCPFailureMethodsMatchSDK derives every method the installed SDK reports to OnError from its source.
// An SDK upgrade that adds, renames or removes one fails here instead of silently alerting "unknown".
func TestMCPFailureMethodsMatchSDK(t *testing.T) {
	fset := token.NewFileSet()
	constants := map[string]mcpgo.MCPMethod{}
	for _, file := range sdkPackageFiles(t, fset, "github.com/mark3labs/mcp-go/mcp") {
		ast.Inspect(file, func(node ast.Node) bool {
			spec, ok := node.(*ast.ValueSpec)
			if !ok {
				return true
			}
			if typ, typed := spec.Type.(*ast.Ident); !typed || typ.Name != "MCPMethod" {
				return true
			}
			for index, name := range spec.Names {
				literal, ok := spec.Values[index].(*ast.BasicLit)
				require.True(t, ok, name.Name)
				value, err := strconv.Unquote(literal.Value)
				require.NoError(t, err)
				constants[name.Name] = mcpgo.MCPMethod(value)
			}
			return true
		})
	}
	require.Equal(t, mcpgo.MethodToolsCall, constants["MethodToolsCall"])

	reported := map[mcpgo.MCPMethod]struct{}{}
	dispatchCases := 0
	for _, file := range sdkPackageFiles(t, fset, "github.com/mark3labs/mcp-go/server") {
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.FuncDecl:
				if (node.Name.Name != "HandleMessage" && node.Name.Name != "handleMessage") || node.Body == nil {
					return true
				}
				// Every request the SDK dispatches reports baseMessage.Method to OnError on failure.
				ast.Inspect(node.Body, func(inner ast.Node) bool {
					dispatch, ok := inner.(*ast.SwitchStmt)
					if !ok || !isSelector(dispatch.Tag, "baseMessage", "Method") {
						return true
					}
					for _, statement := range dispatch.Body.List {
						for _, expr := range statement.(*ast.CaseClause).List {
							selector, ok := expr.(*ast.SelectorExpr)
							require.True(t, ok, fset.Position(expr.Pos()).String())
							method, ok := constants[selector.Sel.Name]
							require.True(t, ok, selector.Sel.Name)
							reported[method] = struct{}{}
							dispatchCases++
						}
					}
					return false
				})
			case *ast.CallExpr:
				callee, ok := node.Fun.(*ast.SelectorExpr)
				if !ok || callee.Sel.Name != "onError" {
					return true
				}
				require.GreaterOrEqual(t, len(node.Args), 3, fset.Position(node.Pos()).String())
				switch method := node.Args[2].(type) {
				case *ast.BasicLit:
					value, err := strconv.Unquote(method.Value)
					require.NoError(t, err)
					reported[mcpgo.MCPMethod(value)] = struct{}{}
				default:
					require.True(t, isSelector(method, "baseMessage", "Method"),
						"new OnError method source at %s must be reviewed for the alert allowlist", fset.Position(method.Pos()))
				}
			}
			return true
		})
	}
	require.Greater(t, dispatchCases, 10, "the SDK request dispatcher must be found")
	require.Contains(t, reported, mcpNotificationFailureMethod)
	require.Equal(t, slices.Sorted(maps.Keys(reported)), slices.Sorted(maps.Keys(mcpFailureMethods)))
}

// TestMCPFailureOTLPCorrelation joins the alerted failure_id to the complete central OTLP record.
func TestMCPFailureOTLPCorrelation(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	previous := activeOTLP.Swap(runtime)
	defer activeOTLP.Store(previous)
	endpoint, messages := alertReceiver(t)
	alert := adoptionAlert(t, endpoint)
	// Production order: the base logger gains the OTLP branch at creation, setupLogger tees the alert core later.
	base, err := logSDK.New(logSDK.WithName(RootLoggerName), logSDK.WithOutputPaths([]string{}), logSDK.WithZapOptions(privateOTLPOption()))
	require.NoError(t, err)
	logger := base.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		return zapcore.NewTee(core, WebDiagnosticAlertCore(alert))
	})).Named(ServiceLoggerScope).Named(MCPHooksLoggerScope).With(zap.String("session_id", "private-session-canary"))
	logger.Error(MCPRequestFailedMessage, zap.Any("request_id", 3), zap.String("method", "tools/call"),
		zap.String("request", "private-request-canary"), zap.Error(errTestPrivate),
		MCPFailureDiagnosticField(MCPFailureSummary{Method: mcpgo.MethodToolsCall, RequestID: float64(3), ErrorKind: MCPFailureToolNotFound, ErrorCode: -32602}))
	message := receivedAlert(t, messages)
	require.NotContains(t, message, "canary")
	alerted, ok := adoptionJSONFields(t, message)[mcpFailureFieldKey].(map[string]any)
	require.True(t, ok)
	stopOTLP(t, runtime)
	records := capture.records()
	require.Len(t, records, 1)
	require.Equal(t, MCPRequestFailedMessage, records[0].Body.GetStringValue())
	attrs := otlpRecordAttributes(records[0].Attributes)
	require.Len(t, attrs[mcpFailureFieldKey], 1)
	central := map[string]string{}
	for _, value := range attrs[mcpFailureFieldKey][0].GetKvlistValue().GetValues() {
		central[value.Key] = value.Value.GetStringValue()
	}
	require.Equal(t, alerted["failure_id"], central["failure_id"], "the alert must join its complete central record")
	require.Equal(t, "tool_not_found", central["error_kind"])
	require.Equal(t, "private-request-canary", attrs["request"][0].GetStringValue(), "central telemetry keeps the complete event")
	require.Equal(t, "private-session-canary", attrs["session_id"][0].GetStringValue())
	require.Equal(t, "private-error-canary", attrs["error"][0].GetStringValue())
}

// errTestPrivate is a synthetic private cause that may appear centrally but never in the alert.
var errTestPrivate = privateTestError("private-error-canary")

// privateTestError is a synthetic error type for correlation tests.
type privateTestError string

// Error returns the synthetic private message.
func (e privateTestError) Error() string { return string(e) }
