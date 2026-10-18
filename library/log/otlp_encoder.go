package log

import (
	"math"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/Laisky/errors/v2"
	"github.com/Laisky/zap/zapcore"
	"go.opentelemetry.io/otel/attribute"
)

var errOTLPRepresentation = errors.New("OTLP log value cannot be represented")

// otlpEncodedField retains one source field and its value before native materialization.
type otlpEncodedField struct {
	key   string
	value any
}

// otlpObjectEncoder records ordered fields and namespace context without executing encoders twice.
type otlpObjectEncoder struct {
	fields  []otlpEncodedField
	current *otlpObjectEncoder
	err     error
}

var _ zapcore.ObjectEncoder = (*otlpObjectEncoder)(nil)

// newOTLPObjectEncoder returns an empty ordered application field encoder.
func newOTLPObjectEncoder() *otlpObjectEncoder {
	encoder := &otlpObjectEncoder{}
	encoder.current = encoder
	return encoder
}

// add appends a supplied field to the currently open namespace.
func (encoder *otlpObjectEncoder) add(key string, value any) {
	encoder.current.fields = append(encoder.current.fields, otlpEncodedField{key: key, value: value})
}

// addFields evaluates each source field once and records private encoder failures for admission.
func (encoder *otlpObjectEncoder) addFields(fields []zapcore.Field) {
	defer func() {
		if recover() != nil {
			encoder.err = errors.New("OTLP log field encoding failed")
		}
	}()
	for _, field := range fields {
		field.AddTo(encoder)
	}
}

// attributes materializes a complete object and applies the official multimap representation.
func (encoder *otlpObjectEncoder) attributes() ([]attribute.KeyValue, error) {
	return encoder.mapFields(true)
}

// mapFields validates only root attribute keys while preserving valid empty keys in nested values.
func (encoder *otlpObjectEncoder) mapFields(root bool) ([]attribute.KeyValue, error) {
	if encoder.err != nil {
		return nil, encoder.err
	}
	attrs := make([]attribute.KeyValue, 0, len(encoder.fields))
	for _, field := range encoder.fields {
		if !utf8.ValidString(field.key) || (root && field.key == "") {
			return nil, errOTLPRepresentation
		}
		value, err := materializeOTLPValue(field.value)
		if err != nil {
			return nil, err
		}
		attrs = append(attrs, attribute.KeyValue{Key: attribute.Key(field.key), Value: value})
	}
	return groupOTLPMultimap(attrs), nil
}

// materializeOTLPValue converts internally recorded nodes without invoking application callbacks.
func materializeOTLPValue(value any) (attribute.Value, error) {
	switch value := value.(type) {
	case attribute.Value:
		return value, nil
	case *otlpObjectEncoder:
		fields, err := value.mapFields(false)
		if err != nil {
			return attribute.Value{}, err
		}
		return attribute.MapValue(fields...), nil
	case *otlpArrayEncoder:
		if value.err != nil {
			return attribute.Value{}, value.err
		}
		items := make([]attribute.Value, 0, len(value.values))
		for _, item := range value.values {
			converted, err := materializeOTLPValue(item)
			if err != nil {
				return attribute.Value{}, err
			}
			items = append(items, converted)
		}
		return attribute.SliceValue(items...), nil
	default:
		return attribute.Value{}, errOTLPRepresentation
	}
}

// groupOTLPMultimap represents every key exactly once and retains source value order in arrays.
// When an object has duplicate keys, all of its keys use arrays, including singleton values.
func groupOTLPMultimap(fields []attribute.KeyValue) []attribute.KeyValue {
	counts := make(map[attribute.Key]int, len(fields))
	duplicate := false
	for _, field := range fields {
		counts[field.Key]++
		duplicate = duplicate || counts[field.Key] > 1
	}
	if !duplicate {
		return fields
	}
	indices := make(map[attribute.Key]int, len(counts))
	grouped := make([]attribute.KeyValue, 0, len(counts))
	values := make([][]attribute.Value, 0, len(counts))
	for _, field := range fields {
		index, exists := indices[field.Key]
		if !exists {
			index = len(grouped)
			indices[field.Key] = index
			grouped = append(grouped, attribute.KeyValue{Key: field.Key})
			values = append(values, nil)
		}
		values[index] = append(values[index], field.Value)
	}
	for index := range grouped {
		grouped[index].Value = attribute.SliceValue(values[index]...)
	}
	return grouped
}

// clone snapshots a logger branch, preserving its active namespace without re-running source encoders.
func (encoder *otlpObjectEncoder) clone() *otlpObjectEncoder {
	clones := make(map[*otlpObjectEncoder]*otlpObjectEncoder)
	var copyValue func(any) any
	var copyObject func(*otlpObjectEncoder) *otlpObjectEncoder
	copyObject = func(source *otlpObjectEncoder) *otlpObjectEncoder {
		target := &otlpObjectEncoder{err: source.err}
		clones[source] = target
		for _, field := range source.fields {
			target.fields = append(target.fields, otlpEncodedField{key: field.key, value: copyValue(field.value)})
		}
		return target
	}
	copyValue = func(value any) any {
		switch value := value.(type) {
		case *otlpObjectEncoder:
			return copyObject(value)
		case *otlpArrayEncoder:
			target := &otlpArrayEncoder{err: value.err}
			for _, item := range value.values {
				target.values = append(target.values, copyValue(item))
			}
			return target
		default:
			return value
		}
	}
	target := copyObject(encoder)
	for source, clone := range clones {
		clone.current = clones[source.current]
	}
	return target
}

// otlpStringValue preserves original string bytes when Unicode is invalid.
func otlpStringValue(value string) attribute.Value {
	if !utf8.ValidString(value) {
		return attribute.ByteSliceValue([]byte(value))
	}
	return attribute.StringValue(value)
}

// otlpUint64Value follows the official decimal-string mapping beyond signed int64.
func otlpUint64Value(value uint64) attribute.Value {
	if value > math.MaxInt64 {
		return attribute.StringValue(strconv.FormatUint(value, 10))
	}
	return attribute.Int64Value(int64(value))
}

// otlpComplexValue preserves complex values through their standard Go string representation.
func otlpComplexValue(value complex128, bits int) attribute.Value {
	return attribute.StringValue(strconv.FormatComplex(value, 'g', -1, bits))
}

// validOTLPKey validates Unicode keys that the SDK can represent without dropping them.
func validOTLPKey(key string) bool { return key != "" && utf8.ValidString(key) }

// AddArray encodes a source array once and retains its node even when the application reports an error.
func (encoder *otlpObjectEncoder) AddArray(key string, value zapcore.ArrayMarshaler) error {
	child := &otlpArrayEncoder{}
	encoder.add(key, child)
	return value.MarshalLogArray(child)
}

// AddObject encodes a source object once and retains normal Zap error-field semantics.
func (encoder *otlpObjectEncoder) AddObject(key string, value zapcore.ObjectMarshaler) error {
	child := newOTLPObjectEncoder()
	encoder.add(key, child)
	return value.MarshalLogObject(child)
}

// AddReflected applies the application's JSON reflection contract to a reflected value.
func (encoder *otlpObjectEncoder) AddReflected(key string, value any) error {
	converted, err := otlpReflectedValue(value)
	if err != nil {
		if errors.Is(err, errOTLPRepresentation) {
			encoder.err = err
		}
		return err
	}
	encoder.add(key, converted)
	return nil
}

// OpenNamespace creates and enters an ordered nested object for subsequent source fields.
func (encoder *otlpObjectEncoder) OpenNamespace(key string) {
	child := newOTLPObjectEncoder()
	encoder.add(key, child)
	encoder.current = child
}

// AddBinary retains the source []byte value in its native OTLP representation.
func (encoder *otlpObjectEncoder) AddBinary(key string, value []byte) {
	encoder.add(key, attribute.ByteSliceValue(value))
}

// AddByteString retains the source []byte value in its native OTLP representation.
func (encoder *otlpObjectEncoder) AddByteString(key string, value []byte) {
	encoder.add(key, otlpStringValue(string(value)))
}

// AddBool retains the source bool value in its native OTLP representation.
func (encoder *otlpObjectEncoder) AddBool(key string, value bool) {
	encoder.add(key, attribute.BoolValue(value))
}

// AddDuration retains the source time.Duration value in its native OTLP representation.
func (encoder *otlpObjectEncoder) AddDuration(key string, value time.Duration) {
	encoder.add(key, attribute.Int64Value(int64(value)))
}

// AddComplex128 retains the source complex128 value in its native OTLP representation.
func (encoder *otlpObjectEncoder) AddComplex128(key string, value complex128) {
	encoder.add(key, otlpComplexValue(value, 128))
}

// AddComplex64 retains the source complex64 value in its native OTLP representation.
func (encoder *otlpObjectEncoder) AddComplex64(key string, value complex64) {
	encoder.add(key, otlpComplexValue(complex128(value), 64))
}

// AddFloat64 retains the source float64 value in its native OTLP representation.
func (encoder *otlpObjectEncoder) AddFloat64(key string, value float64) {
	encoder.add(key, attribute.Float64Value(value))
}

// AddFloat32 retains the source float32 value in its native OTLP representation.
func (encoder *otlpObjectEncoder) AddFloat32(key string, value float32) {
	encoder.add(key, attribute.Float64Value(float64(value)))
}

// AddString retains the source string value in its native OTLP representation.
func (encoder *otlpObjectEncoder) AddString(key string, value string) {
	encoder.add(key, otlpStringValue(value))
}

// AddTime retains the source time.Time value in its native OTLP representation.
func (encoder *otlpObjectEncoder) AddTime(key string, value time.Time) {
	encoder.add(key, attribute.StringValue(value.Format(time.RFC3339Nano)))
}

// AddInt retains the source int value without numeric precision loss.
func (encoder *otlpObjectEncoder) AddInt(key string, value int) {
	encoder.add(key, attribute.Int64Value(int64(value)))
}

// AddInt64 retains the source int64 value without numeric precision loss.
func (encoder *otlpObjectEncoder) AddInt64(key string, value int64) {
	encoder.add(key, attribute.Int64Value(int64(value)))
}

// AddInt32 retains the source int32 value without numeric precision loss.
func (encoder *otlpObjectEncoder) AddInt32(key string, value int32) {
	encoder.add(key, attribute.Int64Value(int64(value)))
}

// AddInt16 retains the source int16 value without numeric precision loss.
func (encoder *otlpObjectEncoder) AddInt16(key string, value int16) {
	encoder.add(key, attribute.Int64Value(int64(value)))
}

// AddInt8 retains the source int8 value without numeric precision loss.
func (encoder *otlpObjectEncoder) AddInt8(key string, value int8) {
	encoder.add(key, attribute.Int64Value(int64(value)))
}

// AddUint retains the source uint value without numeric precision loss.
func (encoder *otlpObjectEncoder) AddUint(key string, value uint) {
	encoder.add(key, otlpUint64Value(uint64(value)))
}

// AddUint64 retains the source uint64 value without numeric precision loss.
func (encoder *otlpObjectEncoder) AddUint64(key string, value uint64) {
	encoder.add(key, otlpUint64Value(uint64(value)))
}

// AddUint32 retains the source uint32 value without numeric precision loss.
func (encoder *otlpObjectEncoder) AddUint32(key string, value uint32) {
	encoder.add(key, otlpUint64Value(uint64(value)))
}

// AddUint16 retains the source uint16 value without numeric precision loss.
func (encoder *otlpObjectEncoder) AddUint16(key string, value uint16) {
	encoder.add(key, otlpUint64Value(uint64(value)))
}

// AddUint8 retains the source uint8 value without numeric precision loss.
func (encoder *otlpObjectEncoder) AddUint8(key string, value uint8) {
	encoder.add(key, otlpUint64Value(uint64(value)))
}

// AddUintptr retains the source uintptr value without numeric precision loss.
func (encoder *otlpObjectEncoder) AddUintptr(key string, value uintptr) {
	encoder.add(key, otlpUint64Value(uint64(value)))
}
