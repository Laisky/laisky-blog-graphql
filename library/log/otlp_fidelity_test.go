package log

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// faithfulOTLPObject supplies application-owned nested fields through Zap's marshaler.
type faithfulOTLPObject struct{}

// MarshalLogObject follows the same field API as the application's stdout encoder.
func (faithfulOTLPObject) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddString("authorization", "Bearer SYNTHETIC_NESTED_AUTH")
	enc.AddInt("count", 7)
	return nil
}

// faithfulOTLPStringer supplies an application-owned string representation.
type faithfulOTLPStringer struct{}

// String supplies the exact value selected by the application field.
func (faithfulOTLPStringer) String() string { return "SYNTHETIC_STRINGER_VALUE" }

// faithfulOTLPJSON exercises reflected JSON tags and a custom marshaler.
type faithfulOTLPJSON struct{}

// MarshalJSON supplies the application's reflected field representation.
func (faithfulOTLPJSON) MarshalJSON() ([]byte, error) {
	return []byte(`{"tagged":{"payload":"SYNTHETIC_REFLECTED_BODY"},"items":[1,true,null]}`), nil
}

// otlpRecordAttributes retains duplicate keys for assertions about the wire representation.
func otlpRecordAttributes(attrs []*commonpb.KeyValue) map[string][]*commonpb.AnyValue {
	values := make(map[string][]*commonpb.AnyValue)
	for _, attr := range attrs {
		values[attr.Key] = append(values[attr.Key], attr.Value)
	}
	return values
}

// TestOTLPFaithfulDefault preserves application-provided messages and fields without transport redaction.
func TestOTLPFaithfulDefault(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	previous := activeOTLP.Swap(runtime)
	defer activeOTLP.Store(previous)
	logger, err := logSDK.New(logSDK.WithName("graphql"), logSDK.WithOutputPaths([]string{}), logSDK.WithZapOptions(privateOTLPOption()))
	require.NoError(t, err)
	message := "operational event password=SYNTHETIC_MESSAGE_VALUE " + syntheticOTLPToken
	logger.With(zap.String("bound_context", "SYNTHETIC_CONTEXT_VALUE")).Info(message,
		zap.String("api_key", "SYNTHETIC_API_VALUE"), zap.String("authorization", "Bearer SYNTHETIC_AUTH_VALUE"),
		zap.String("request_body", "SYNTHETIC_REQUEST_VALUE"), zap.String("current_input", "SYNTHETIC_INPUT_VALUE"),
		zap.String("url", "https://user:SYNTHETIC_URL_VALUE@example.invalid/private?token=SYNTHETIC_QUERY_VALUE"),
		zap.String("future_unknown_key", "future-enum-v123"), zap.Error(io.EOF))
	stopOTLP(t, runtime)
	records := capture.records()
	require.Len(t, records, 1)
	require.Equal(t, message, records[0].Body.GetStringValue())
	attrs := otlpRecordAttributes(records[0].Attributes)
	for key, value := range map[string]string{
		"bound_context": "SYNTHETIC_CONTEXT_VALUE", "api_key": "SYNTHETIC_API_VALUE", "authorization": "Bearer SYNTHETIC_AUTH_VALUE",
		"request_body": "SYNTHETIC_REQUEST_VALUE", "current_input": "SYNTHETIC_INPUT_VALUE",
		"url":                "https://user:SYNTHETIC_URL_VALUE@example.invalid/private?token=SYNTHETIC_QUERY_VALUE",
		"future_unknown_key": "future-enum-v123", "error": "EOF",
	} {
		require.Len(t, attrs[key], 1, key)
		require.Equal(t, value, attrs[key][0].GetStringValue(), key)
	}
}

// TestOTLPStructuredFieldsUseZapSemantics preserves nested data, callbacks, namespaces and duplicate keys.
func TestOTLPStructuredFieldsUseZapSemantics(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	require.NoError(t, runtime.emit(zapcore.Entry{Level: zapcore.InfoLevel, Time: time.Now(), Message: "nested application event"}, []zapcore.Field{
		zap.Object("object", faithfulOTLPObject{}), zap.Reflect("reflected", faithfulOTLPJSON{}),
		zap.Stringer("stringer", faithfulOTLPStringer{}), zap.Binary("binary", []byte{0, 1, 255}), zap.ByteString("bytes", []byte("raw bytes")),
		zap.Strings("array", []string{"SYNTHETIC_ARRAY_VALUE", "second"}), zap.Uint64("large_uint", math.MaxUint64),
		zap.Complex128("complex", complex(1, 2)), zap.Float64("nonfinite", math.Inf(1)),

		zap.Reflect("null", nil), zap.Reflect("empty_map", map[string]any{}), zap.Reflect("empty_array", []any{}),
		zap.String("service.name", "SYNTHETIC_FIELD_SERVICE"), zap.String("_msg", "SYNTHETIC_FIELD_MESSAGE"),
		zap.Namespace("request"), zap.String("message", "SYNTHETIC_NAMESPACE_VALUE"), zap.Object("details", faithfulOTLPObject{}),
	}))
	stopOTLP(t, runtime)
	records := capture.records()
	require.Len(t, records, 1)
	attrs := otlpRecordAttributes(records[0].Attributes)
	require.Equal(t, "SYNTHETIC_NESTED_AUTH", strings.TrimPrefix(otlpRecordAttributes(attrs["object"][0].GetKvlistValue().Values)["authorization"][0].GetStringValue(), "Bearer "))
	require.NotNil(t, attrs["reflected"][0].GetKvlistValue())
	require.Equal(t, "SYNTHETIC_STRINGER_VALUE", attrs["stringer"][0].GetStringValue())
	require.Equal(t, []byte{0, 1, 255}, attrs["binary"][0].GetBytesValue())
	require.Equal(t, "raw bytes", attrs["bytes"][0].GetStringValue())
	require.Equal(t, "SYNTHETIC_ARRAY_VALUE", attrs["array"][0].GetArrayValue().Values[0].GetStringValue())
	require.Equal(t, "18446744073709551615", attrs["large_uint"][0].GetStringValue())
	require.Equal(t, "(1+2i)", attrs["complex"][0].GetStringValue())
	require.True(t, math.IsInf(attrs["nonfinite"][0].GetDoubleValue(), 1))
	require.Len(t, attrs["null"], 1)
	require.Nil(t, attrs["null"][0].Value)
	require.NotNil(t, attrs["empty_map"][0].GetKvlistValue())
	require.Empty(t, attrs["empty_map"][0].GetKvlistValue().Values)
	require.NotNil(t, attrs["empty_array"][0].GetArrayValue())
	require.Empty(t, attrs["empty_array"][0].GetArrayValue().Values)
	require.Equal(t, "SYNTHETIC_FIELD_SERVICE", attrs["service.name"][0].GetStringValue())
	require.Equal(t, "SYNTHETIC_FIELD_MESSAGE", attrs["_msg"][0].GetStringValue())
	request := otlpRecordAttributes(attrs["request"][0].GetKvlistValue().Values)
	require.Equal(t, "SYNTHETIC_NAMESPACE_VALUE", request["message"][0].GetStringValue())
	require.NotNil(t, request["details"][0].GetKvlistValue())
}

// TestOTLPContentIsNotTruncated guards the absence of implicit field-count and text limits.
func TestOTLPContentIsNotTruncated(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	message := strings.Repeat("original application content ", 400)
	value := strings.Repeat("complete structured value ", 150)
	fields := make([]zapcore.Field, 0, 150)
	for i := range 150 {
		fields = append(fields, zap.String(fmt.Sprintf("future_field_%d", i), value))
	}
	require.NoError(t, runtime.emit(zapcore.Entry{Level: zapcore.InfoLevel, Time: time.Now(), Message: message}, fields))
	stopOTLP(t, runtime)
	records := capture.records()
	require.Len(t, records, 1)
	require.Equal(t, message, records[0].Body.GetStringValue())
	require.Len(t, records[0].Attributes, 150)
	attrs := otlpRecordAttributes(records[0].Attributes)
	for i := range 150 {
		require.Equal(t, value, attrs[fmt.Sprintf("future_field_%d", i)][0].GetStringValue())
	}
}

// panickingOTLPObject verifies failed application encoding rejects the whole remote record.
type panickingOTLPObject struct{}

// MarshalLogObject deliberately panics with a synthetic value.
func (panickingOTLPObject) MarshalLogObject(zapcore.ObjectEncoder) error {
	panic("SYNTHETIC_CALLBACK_PANIC")
}

// TestOTLPProtocolRejectionIsVisible reports an unrepresentable record rather than silently rewriting fields.
func TestOTLPProtocolRejectionIsVisible(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	require.Error(t, runtime.emit(zapcore.Entry{Level: zapcore.InfoLevel, Time: time.Now(), Message: "callback failed"}, []zapcore.Field{zap.Object("object", panickingOTLPObject{})}))
	require.Equal(t, uint64(1), runtime.stats().Rejected)
	require.Zero(t, runtime.stats().Emitted)
	require.Zero(t, capture.attempts.Load())
	require.NoError(t, runtime.emit(zapcore.Entry{Level: zapcore.InfoLevel, Time: time.Now(), Message: "valid after rejection"}, nil))
	stopOTLP(t, runtime)
	require.Len(t, capture.records(), 1)
	require.Equal(t, "valid after rejection", capture.records()[0].Body.GetStringValue())
	require.Equal(t, OTLPStats{Emitted: 1, ExportedRecords: 1, Rejected: 1}, runtime.stats())
}

// TestOTLPResourceEnvironmentPreserved retains custom application resource attributes and explicit identity precedence.
func TestOTLPResourceEnvironmentPreserved(t *testing.T) {
	custom := strings.Repeat("SYNTHETIC_RESOURCE_VALUE", 1000)
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "custom.application.resource="+custom+",chosen.attribute=chosen%20value,service.name=chosen-service,host.name=chosen-host")
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	require.NoError(t, runtime.emit(zapcore.Entry{Time: time.Now(), Level: zapcore.InfoLevel, Message: "resource fidelity"}, nil))
	stopOTLP(t, runtime)
	capture.mu.Lock()
	defer capture.mu.Unlock()
	require.Len(t, capture.requests, 1)
	attrs := otlpRecordAttributes(capture.requests[0].ResourceLogs[0].Resource.Attributes)
	require.Equal(t, custom, attrs["custom.application.resource"][0].GetStringValue())
	require.Equal(t, "chosen value", attrs["chosen.attribute"][0].GetStringValue())
	require.Equal(t, "graphql", attrs["service.name"][0].GetStringValue())
	require.Equal(t, "test-b1", attrs["host.name"][0].GetStringValue())
}

// TestOTLPRejectionUsesConfiguredErrorOutput leaves stdout and the application's error destination intact.
func TestOTLPRejectionUsesConfiguredErrorOutput(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	previous := activeOTLP.Swap(runtime)
	defer activeOTLP.Store(previous)
	var output bytes.Buffer
	logger, err := logSDK.New(logSDK.WithOutputPaths([]string{}), logSDK.WithZapOptions(privateOTLPOption(), zap.ErrorOutput(zapcore.AddSync(&output))))
	require.NoError(t, err)
	var callbacks atomic.Int64
	logger.Info("callback failure", zap.Object("object", secondCallPanicsOTLPObject{&callbacks}))
	stopOTLP(t, runtime)
	require.Contains(t, output.String(), "OTLP log field encoding failed")
	require.Equal(t, uint64(1), runtime.stats().Rejected)
	require.Zero(t, runtime.stats().Emitted)
	require.Zero(t, capture.attempts.Load())
}

// TestOTLPChosenResourceIdentityIsNotRewritten retains explicitly configured Unicode and long labels.
func TestOTLPChosenResourceIdentityIsNotRewritten(t *testing.T) {
	capture := new(otlpCapture)
	config := otlpFixture(t, capture)
	config.host = " chosen host " + strings.Repeat("\u6d4b\u8bd5", 100)
	config.environment = " custom environment " + strings.Repeat("chosen-value-", 30)
	runtime, err := newOTLPRuntime(context.Background(), config)
	require.NoError(t, err)
	require.NoError(t, runtime.emit(zapcore.Entry{Time: time.Now(), Level: zapcore.InfoLevel, Message: "chosen resource identity"}, nil))
	stopOTLP(t, runtime)
	capture.mu.Lock()
	defer capture.mu.Unlock()
	attrs := otlpRecordAttributes(capture.requests[0].ResourceLogs[0].Resource.Attributes)
	require.Equal(t, config.host, attrs["host.name"][0].GetStringValue())
	require.Equal(t, config.environment, attrs["deployment.environment.name"][0].GetStringValue())
}

// TestOTLPNumberFallbackRetainsExactValues uses official AnyValue strings for numbers outside IEEE-64 representation.
func TestOTLPNumberFallbackRetainsExactValues(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	entry := zapcore.Entry{Time: time.Now(), Level: zapcore.InfoLevel, Message: "numeric representation"}
	require.NoError(t, runtime.emit(entry, []zapcore.Field{
		zap.Reflect("underflow", json.RawMessage("1e-1000")), zap.Reflect("overflow", json.RawMessage("1e1000")),
		zap.Reflect("precision", json.RawMessage("0.123456789012345678901")),
		zap.Float64("negative_zero", math.Copysign(0, -1)), zap.Reflect("zero_exponent", json.RawMessage("0e-1000")),
	}))
	stopOTLP(t, runtime)
	records := capture.records()
	require.Len(t, records, 1)
	attrs := otlpRecordAttributes(records[0].Attributes)
	for key, value := range map[string]string{"underflow": "1e-1000", "overflow": "1e1000", "precision": "0.123456789012345678901"} {
		require.Equal(t, value, attrs[key][0].GetStringValue())
	}
	require.True(t, math.Signbit(attrs["negative_zero"][0].GetDoubleValue()))
	require.Zero(t, attrs["zero_exponent"][0].GetDoubleValue())
	require.Equal(t, OTLPStats{Emitted: 1, ExportedRecords: 1}, runtime.stats())
}

// TestOTLPReflectedUnicodeDoesNotSilentlyChange preserves raw bytes and rejects lone surrogate escapes.
func TestOTLPReflectedUnicodeDoesNotSilentlyChange(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	entry := zapcore.Entry{Time: time.Now(), Level: zapcore.InfoLevel, Message: "Unicode representation"}
	for _, raw := range []json.RawMessage{json.RawMessage(`"\ud800"`), json.RawMessage(`"\udc00"`)} {
		require.Error(t, runtime.emit(entry, []zapcore.Field{zap.Reflect("raw", raw)}))
	}
	require.Equal(t, uint64(2), runtime.stats().Rejected)
	require.Zero(t, runtime.stats().Emitted)
	require.NoError(t, runtime.emit(entry, []zapcore.Field{
		zap.Reflect("raw_bytes", json.RawMessage([]byte{'"', 255, '"'})),
		zap.Reflect("emoji", json.RawMessage(`"\ud83d\ude00"`)),
		zap.Reflect("literal_escape", json.RawMessage(`"\\ud800"`)),
		zap.String("source_replacement", string([]byte{255})),
	}))
	stopOTLP(t, runtime)
	records := capture.records()
	require.Len(t, records, 1)
	attrs := otlpRecordAttributes(records[0].Attributes)
	require.Equal(t, []byte{255}, attrs["raw_bytes"][0].GetBytesValue())
	require.Equal(t, "\U0001f600", attrs["emoji"][0].GetStringValue())
	require.Equal(t, `\ud800`, attrs["literal_escape"][0].GetStringValue())
	require.Equal(t, []byte{255}, attrs["source_replacement"][0].GetBytesValue())
}

// stalledOTLPObject holds only synthetic callback work while provider shutdown proceeds.
type stalledOTLPObject struct{ entered, release chan struct{} }

// MarshalLogObject exposes a controlled stall without network or production work.
func (v stalledOTLPObject) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	close(v.entered)
	<-v.release
	enc.AddString("value", "synthetic callback complete")
	return nil
}

// TestOTLPShutdownDoesNotWaitForFieldCallbacks bounds shutdown even if a source callback stalls.
func TestOTLPShutdownDoesNotWaitForFieldCallbacks(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	outcome := make(chan error, 1)
	go func() {
		outcome <- runtime.emit(zapcore.Entry{Time: time.Now(), Level: zapcore.InfoLevel, Message: "callback pending"}, []zapcore.Field{zap.Object("object", stalledOTLPObject{entered, release})})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("callback did not enter")
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- runtime.shutdown(context.Background()) }()
	select {
	case err := <-shutdownDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for application callback")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-outcome:
		require.ErrorContains(t, err, "stopped before record admission")
	case <-time.After(time.Second):
		t.Fatal("callback did not complete")
	}
	require.Equal(t, OTLPStats{Rejected: 1}, runtime.stats())
	require.Zero(t, capture.attempts.Load())
}

// secondCallPanicsOTLPObject succeeds in stdout and fails in the optional output branch.
type secondCallPanicsOTLPObject struct{ calls *atomic.Int64 }

// MarshalLogObject supplies a controlled private-branch callback failure.
func (v secondCallPanicsOTLPObject) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	if v.calls.Add(1) == 2 {
		panic("SYNTHETIC_PRIVATE_CALLBACK_FAILURE")
	}
	enc.AddString("value", "source succeeded")
	return nil
}

// TestOTLPInvalidUnicodeUsesBytes preserves original source bytes using the official AnyValue mapping.
func TestOTLPInvalidUnicodeUsesBytes(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	original := []byte{255, 0, 254}
	require.NoError(t, runtime.emit(zapcore.Entry{Time: time.Now(), Level: zapcore.InfoLevel, Message: string(original)}, []zapcore.Field{zap.String("value", string(original))}))
	stopOTLP(t, runtime)
	records := capture.records()
	require.Len(t, records, 1)
	require.Equal(t, original, records[0].Body.GetBytesValue())
	attrs := otlpRecordAttributes(records[0].Attributes)
	require.Equal(t, original, attrs["value"][0].GetBytesValue())
	require.Zero(t, runtime.stats().Rejected)
}

// emptyKeyOTLPObject exercises valid empty names inside a native AnyValue map.
type emptyKeyOTLPObject struct{}

// MarshalLogObject retains an application-authored empty nested field name.
func (emptyKeyOTLPObject) MarshalLogObject(encoder zapcore.ObjectEncoder) error {
	encoder.AddString("", "synthetic nested empty key")
	return nil
}

// TestOTLPNestedEmptyKeysSurvive distinguishes valid map keys from SDK record-attribute rejection.
func TestOTLPNestedEmptyKeysSurvive(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	entry := zapcore.Entry{Time: time.Now(), Level: zapcore.InfoLevel, Message: "nested empty key"}
	require.NoError(t, runtime.emit(entry, []zapcore.Field{
		zap.Object("object", emptyKeyOTLPObject{}),
		zap.Reflect("reflected", map[string]any{"": "synthetic reflected empty key"}),
	}))
	stopOTLP(t, runtime)
	records := capture.records()
	require.Len(t, records, 1)
	attrs := otlpRecordAttributes(records[0].Attributes)
	require.Equal(t, "synthetic nested empty key", otlpRecordAttributes(attrs["object"][0].GetKvlistValue().Values)[""][0].GetStringValue())
	require.Equal(t, "synthetic reflected empty key", otlpRecordAttributes(attrs["reflected"][0].GetKvlistValue().Values)[""][0].GetStringValue())
	require.Zero(t, runtime.stats().Rejected)
}
