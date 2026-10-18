package log

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/Laisky/errors/v2"
	"go.opentelemetry.io/otel/attribute"
)

// otlpReflectedValue applies normal application JSON reflection semantics once.
// Application marshal errors remain ordinary Zap error fields; malformed protocol values reject admission.
func otlpReflectedValue(value any) (attribute.Value, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return attribute.Value{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	converted, err := decodeOTLPJSONValue(decoder, data)
	if err != nil {
		return attribute.Value{}, errOTLPRepresentation
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return attribute.Value{}, errOTLPRepresentation
	}
	return converted, nil
}

// decodeOTLPJSONValue maps reflected JSON recursively without normalizing source string bytes.
func decodeOTLPJSONValue(decoder *json.Decoder, source []byte) (attribute.Value, error) {
	start := decoder.InputOffset()
	token, err := decoder.Token()
	if err != nil {
		return attribute.Value{}, errOTLPRepresentation
	}
	switch value := token.(type) {
	case nil:
		return attribute.Value{}, nil
	case bool:
		return attribute.BoolValue(value), nil
	case string:
		original, err := originalOTLPJSONString(source[start:decoder.InputOffset()])
		if err != nil {
			return attribute.Value{}, err
		}
		return otlpStringValue(string(original)), nil
	case json.Number:
		return otlpJSONNumber(value), nil
	case json.Delim:
		switch value {
		case '{':
			var fields []attribute.KeyValue
			for decoder.More() {
				start := decoder.InputOffset()
				key, err := decoder.Token()
				if err != nil {
					return attribute.Value{}, errOTLPRepresentation
				}
				if _, ok := key.(string); !ok {
					return attribute.Value{}, errOTLPRepresentation
				}
				original, err := originalOTLPJSONString(source[start:decoder.InputOffset()])
				if err != nil || !utf8.Valid(original) {
					return attribute.Value{}, errOTLPRepresentation
				}
				child, err := decodeOTLPJSONValue(decoder, source)
				if err != nil {
					return attribute.Value{}, err
				}
				fields = append(fields, attribute.KeyValue{Key: attribute.Key(original), Value: child})
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
				return attribute.Value{}, errOTLPRepresentation
			}
			return attribute.MapValue(groupOTLPMultimap(fields)...), nil
		case '[':
			var values []attribute.Value
			for decoder.More() {
				child, err := decodeOTLPJSONValue(decoder, source)
				if err != nil {
					return attribute.Value{}, err
				}
				values = append(values, child)
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
				return attribute.Value{}, errOTLPRepresentation
			}
			return attribute.SliceValue(values...), nil
		}
	}
	return attribute.Value{}, errOTLPRepresentation
}

// otlpJSONNumber retains decimal values outside double range or shortest-roundtrip precision as strings.
func otlpJSONNumber(value json.Number) attribute.Value {
	if value == "-0" {
		return attribute.Float64Value(math.Copysign(0, -1))
	}
	integer, err := value.Int64()
	if err != nil {
		if !strings.ContainsAny(string(value), ".eE") {
			return attribute.StringValue(string(value))
		}
		number, err := strconv.ParseFloat(string(value), 64)
		if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
			return attribute.StringValue(string(value))
		}
		if number == 0 {
			mantissa, _, _ := strings.Cut(strings.ToLower(string(value)), "e")
			if strings.ContainsAny(mantissa, "123456789") {
				return attribute.StringValue(string(value))
			}
			return attribute.Float64Value(number)
		}
		var original, rounded big.Rat
		if _, ok := original.SetString(string(value)); !ok {
			return attribute.StringValue(string(value))
		}
		if _, ok := rounded.SetString(strconv.FormatFloat(number, 'g', -1, 64)); !ok {
			return attribute.StringValue(string(value))
		}
		if original.Cmp(&rounded) != 0 {
			return attribute.StringValue(string(value))
		}
		return attribute.Float64Value(number)
	}
	return attribute.Int64Value(integer)
}

// originalOTLPJSONString unescapes one JSON string while retaining original invalid UTF-8 bytes.
// Lone UTF-16 surrogate escapes have no original Unicode scalar value and are explicitly rejected.
func originalOTLPJSONString(token []byte) ([]byte, error) {
	token = bytes.TrimLeft(token, " \t\r\n:,")
	if len(token) < 2 || token[0] != '"' || token[len(token)-1] != '"' {
		return nil, errOTLPRepresentation
	}
	token = token[1 : len(token)-1]
	value := make([]byte, 0, len(token))
	for index := 0; index < len(token); index++ {
		if token[index] != '\\' {
			value = append(value, token[index])
			continue
		}
		index++
		if index >= len(token) {
			return nil, errOTLPRepresentation
		}
		switch token[index] {
		case '"', '\\', '/':
			value = append(value, token[index])
		case 'b':
			value = append(value, '\b')
		case 'f':
			value = append(value, '\f')
		case 'n':
			value = append(value, '\n')
		case 'r':
			value = append(value, '\r')
		case 't':
			value = append(value, '\t')
		case 'u':
			if index+4 >= len(token) {
				return nil, errOTLPRepresentation
			}
			code, err := strconv.ParseUint(string(token[index+1:index+5]), 16, 16)
			if err != nil {
				return nil, errOTLPRepresentation
			}
			index += 4
			scalar := rune(code)
			if code >= 0xD800 && code <= 0xDBFF {
				if index+6 >= len(token) || token[index+1] != '\\' || token[index+2] != 'u' {
					return nil, errOTLPRepresentation
				}
				low, err := strconv.ParseUint(string(token[index+3:index+7]), 16, 16)
				if err != nil || low < 0xDC00 || low > 0xDFFF {
					return nil, errOTLPRepresentation
				}
				scalar = utf16.DecodeRune(scalar, rune(low))
				index += 6
			} else if code >= 0xDC00 && code <= 0xDFFF {
				return nil, errOTLPRepresentation
			}
			value = utf8.AppendRune(value, scalar)
		default:
			return nil, errOTLPRepresentation
		}
	}
	return value, nil
}
