package log

import (
	"time"

	"github.com/Laisky/errors/v2"
	"github.com/Laisky/zap/zapcore"
	"go.opentelemetry.io/otel/attribute"
)

// otlpArrayEncoder retains ordered native values and nested encoder nodes.
type otlpArrayEncoder struct {
	values []any
	err    error
}

var _ zapcore.ArrayEncoder = (*otlpArrayEncoder)(nil)

// AppendBool appends value as a native OTLP boolean.
func (e *otlpArrayEncoder) AppendBool(value bool) {
	e.values = append(e.values, attribute.BoolValue(value))
}

// AppendByteString appends value as text or bytes when its UTF-8 is invalid.
func (e *otlpArrayEncoder) AppendByteString(value []byte) {
	e.values = append(e.values, otlpStringValue(string(value)))
}

// AppendComplex128 appends value using the shared full-precision complex representation.
func (e *otlpArrayEncoder) AppendComplex128(value complex128) {
	e.values = append(e.values, otlpComplexValue(value, 128))
}

// AppendComplex64 appends value using the shared complex64 representation.
func (e *otlpArrayEncoder) AppendComplex64(value complex64) {
	e.values = append(e.values, otlpComplexValue(complex128(value), 64))
}

// AppendFloat64 appends value as an OTLP double, including nonfinite values.
func (e *otlpArrayEncoder) AppendFloat64(value float64) {
	e.values = append(e.values, attribute.Float64Value(value))
}

// AppendFloat32 appends value as an OTLP double without discarding its sign.
func (e *otlpArrayEncoder) AppendFloat32(value float32) {
	e.values = append(e.values, attribute.Float64Value(float64(value)))
}

// AppendInt appends value as a signed OTLP integer.
func (e *otlpArrayEncoder) AppendInt(value int) {
	e.values = append(e.values, attribute.IntValue(value))
}

// AppendInt64 appends value as a signed OTLP integer.
func (e *otlpArrayEncoder) AppendInt64(value int64) {
	e.values = append(e.values, attribute.Int64Value(value))
}

// AppendInt32 appends value as a signed OTLP integer.
func (e *otlpArrayEncoder) AppendInt32(value int32) {
	e.values = append(e.values, attribute.Int64Value(int64(value)))
}

// AppendInt16 appends value as a signed OTLP integer.
func (e *otlpArrayEncoder) AppendInt16(value int16) {
	e.values = append(e.values, attribute.Int64Value(int64(value)))
}

// AppendInt8 appends value as a signed OTLP integer.
func (e *otlpArrayEncoder) AppendInt8(value int8) {
	e.values = append(e.values, attribute.Int64Value(int64(value)))
}

// AppendString appends value as text or bytes when its UTF-8 is invalid.
func (e *otlpArrayEncoder) AppendString(value string) {
	e.values = append(e.values, otlpStringValue(value))
}

// AppendUint appends value using the shared lossless unsigned representation.
func (e *otlpArrayEncoder) AppendUint(value uint) {
	e.values = append(e.values, otlpUint64Value(uint64(value)))
}

// AppendUint64 appends value using the shared lossless unsigned representation.
func (e *otlpArrayEncoder) AppendUint64(value uint64) {
	e.values = append(e.values, otlpUint64Value(value))
}

// AppendUint32 appends value using the shared lossless unsigned representation.
func (e *otlpArrayEncoder) AppendUint32(value uint32) {
	e.values = append(e.values, otlpUint64Value(uint64(value)))
}

// AppendUint16 appends value using the shared lossless unsigned representation.
func (e *otlpArrayEncoder) AppendUint16(value uint16) {
	e.values = append(e.values, otlpUint64Value(uint64(value)))
}

// AppendUint8 appends value using the shared lossless unsigned representation.
func (e *otlpArrayEncoder) AppendUint8(value uint8) {
	e.values = append(e.values, otlpUint64Value(uint64(value)))
}

// AppendUintptr appends value using the shared lossless unsigned representation.
func (e *otlpArrayEncoder) AppendUintptr(value uintptr) {
	e.values = append(e.values, otlpUint64Value(uint64(value)))
}

// AppendDuration appends value as signed nanoseconds without unit conversion.
func (e *otlpArrayEncoder) AppendDuration(value time.Duration) {
	e.values = append(e.values, attribute.Int64Value(int64(value)))
}

// AppendTime appends value as RFC3339Nano text retaining its original offset.
func (e *otlpArrayEncoder) AppendTime(value time.Time) {
	e.values = append(e.values, otlpStringValue(value.Format(time.RFC3339Nano)))
}

// AppendArray appends an ordered child node, invokes value once and returns its error.
func (e *otlpArrayEncoder) AppendArray(value zapcore.ArrayMarshaler) error {
	child := new(otlpArrayEncoder)
	e.values = append(e.values, child)
	return value.MarshalLogArray(child)
}

// AppendObject appends an ordered child node, invokes value once and returns its error.
func (e *otlpArrayEncoder) AppendObject(value zapcore.ObjectMarshaler) error {
	child := newOTLPObjectEncoder()
	e.values = append(e.values, child)
	return value.MarshalLogObject(child)
}

// AppendReflected converts value once, appends its representation and returns any conversion error.
func (e *otlpArrayEncoder) AppendReflected(value any) error {
	converted, err := otlpReflectedValue(value)
	if err != nil {
		if errors.Is(err, errOTLPRepresentation) {
			e.err = err
		}
		return err
	}
	e.values = append(e.values, converted)
	return nil
}
