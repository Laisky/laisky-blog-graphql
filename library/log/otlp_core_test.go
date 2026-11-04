package log

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"
)

// countingOTLPBoundObject exposes whether bound context is evaluated again during writes.
type countingOTLPBoundObject struct {
	value *string
	calls *atomic.Int64
}

// MarshalLogObject snapshots the chosen value and counts each output branch's binding.
func (v countingOTLPBoundObject) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	v.calls.Add(1)
	enc.AddString("value", *v.value)
	return nil
}

// newOTLPTestJSONLogger creates a source JSON branch followed by the production optional output.
func newOTLPTestJSONLogger(t *testing.T, output *bytes.Buffer) logSDK.Logger {
	t.Helper()
	config := zap.NewProductionEncoderConfig()
	config.EncodeTime = zapcore.RFC3339TimeEncoder
	logger, err := logSDK.New(logSDK.WithName("graphql"), logSDK.WithZapOptions(zap.WrapCore(func(zapcore.Core) zapcore.Core {
		return zapcore.NewCore(zapcore.NewJSONEncoder(config), zapcore.AddSync(output), zapcore.DebugLevel)
	}), privateOTLPOption()))
	require.NoError(t, err)
	return logger
}

// TestOTLPDisabledBranchDoesNotEvaluateCallbacks preserves the original disabled logger behavior.
func TestOTLPDisabledBranchDoesNotEvaluateCallbacks(t *testing.T) {
	t.Setenv("APP_LOG_OTLP_ENDPOINT", "")
	previous := activeOTLP.Swap(nil)
	defer activeOTLP.Store(previous)
	var output bytes.Buffer
	var calls atomic.Int64
	value := "chosen application value"
	logger := newOTLPTestJSONLogger(t, &output).With(zap.Object("object", countingOTLPBoundObject{&value, &calls}))
	require.Equal(t, int64(1), calls.Load())
	logger.Info("one")
	logger.Info("two")
	require.Equal(t, int64(1), calls.Load())
	require.Contains(t, output.String(), value)
}

// TestOTLPWithSnapshotsNamespacesAndFieldOrder retains bound context once per opted-in output branch.
func TestOTLPWithSnapshotsNamespacesAndFieldOrder(t *testing.T) {
	capture := new(otlpCapture)
	runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
	require.NoError(t, err)
	previous := activeOTLP.Swap(runtime)
	defer activeOTLP.Store(previous)
	var output bytes.Buffer
	var calls atomic.Int64
	value := "value at With"
	logger := newOTLPTestJSONLogger(t, &output)
	child := logger.With(zap.Namespace("request"), zap.String("tenant", "synthetic tenant")).With(zap.Object("object", countingOTLPBoundObject{&value, &calls}))
	require.Equal(t, int64(2), calls.Load())
	value = "value after With"
	child.Info("one", zap.String("message", "call-site one"))
	child.Info("two", zap.String("message", "call-site two"))
	logger.With(zap.String("duplicate", "bound-first")).Info("duplicate order", zap.String("duplicate", "call-second"), zap.Int("unique", 7))
	stopOTLP(t, runtime)
	require.Equal(t, int64(2), calls.Load())
	require.NotContains(t, output.String(), "value after With")
	records := capture.records()
	require.Len(t, records, 3)
	for i := range 2 {
		attrs := otlpRecordAttributes(records[i].Attributes)
		require.NotContains(t, attrs, "message")
		request := otlpRecordAttributes(attrs["request"][0].GetKvlistValue().Values)
		require.Equal(t, "synthetic tenant", request["tenant"][0].GetStringValue())
		object := otlpRecordAttributes(request["object"][0].GetKvlistValue().Values)
		require.Equal(t, "value at With", object["value"][0].GetStringValue())
		require.Contains(t, request["message"][0].GetStringValue(), "call-site")
	}
	attrs := otlpRecordAttributes(records[2].Attributes)
	require.Len(t, attrs["duplicate"], 1)
	values := attrs["duplicate"][0].GetArrayValue().Values
	require.Len(t, values, 2)
	require.Equal(t, "bound-first", values[0].GetStringValue())
	require.Equal(t, "call-second", values[1].GetStringValue())
	require.Len(t, attrs["unique"], 1)
	require.Equal(t, int64(7), attrs["unique"][0].GetArrayValue().Values[0].GetIntValue())
}

// decisionOTLPCore supplies one counted writer-acceptance decision without field callbacks.
type decisionOTLPCore struct {
	accept         bool
	checks, writes *atomic.Int64
}

// Enabled leaves writer admission to Check for sampler/filter regression coverage.
func (c decisionOTLPCore) Enabled(zapcore.Level) bool { return true }

// With preserves this fixture's unchanged field-free Core.
func (c decisionOTLPCore) With([]zapcore.Field) zapcore.Core { return c }

// Fields returns no synthetic bound context for this fixture.
func (c decisionOTLPCore) Fields() []zapcore.Field { return nil }

// Sync has no buffered output in this fixture.
func (c decisionOTLPCore) Sync() error { return nil }

// Check counts exactly one decision and can decline an already accepted sibling entry.
func (c decisionOTLPCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	c.checks.Add(1)
	if c.accept {
		return ce.AddCore(entry, c)
	}
	return ce
}

// Write records exactly one accepted source write.
func (c decisionOTLPCore) Write(zapcore.Entry, []zapcore.Field) error { c.writes.Add(1); return nil }

// TestOTLPCorePreservesExistingCheckedEntryDecision does not mistake a tee sibling for base admission.
func TestOTLPCorePreservesExistingCheckedEntryDecision(t *testing.T) {
	for _, accept := range []bool{false, true} {
		t.Run(map[bool]string{false: "declined", true: "accepted"}[accept], func(t *testing.T) {
			capture := new(otlpCapture)
			runtime, err := newOTLPRuntime(context.Background(), otlpFixture(t, capture))
			require.NoError(t, err)
			previous := activeOTLP.Swap(runtime)
			defer activeOTLP.Store(previous)
			var checks, writes, siblingChecks, siblingWrites atomic.Int64
			base := decisionOTLPCore{accept, &checks, &writes}
			sibling := decisionOTLPCore{true, &siblingChecks, &siblingWrites}
			core := newOTLPContextCore(base, newOTLPObjectEncoder())
			entry := zapcore.Entry{Time: time.Now(), Level: zapcore.InfoLevel, Message: "gate decision"}
			var ce *zapcore.CheckedEntry
			ce = sibling.Check(entry, ce)
			ce = core.Check(entry, ce)
			require.NotNil(t, ce)
			ce.Write()
			stopOTLP(t, runtime)
			require.Equal(t, int64(1), checks.Load())
			require.Equal(t, int64(1), siblingWrites.Load())
			expected := int64(0)
			if accept {
				expected = 1
			}
			require.Equal(t, expected, writes.Load())
			require.Equal(t, uint64(expected), runtime.stats().Emitted)
			require.Len(t, capture.records(), int(expected))
		})
	}
}
