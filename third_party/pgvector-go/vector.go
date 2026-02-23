package pgvector

import (
	"database/sql/driver"
	"encoding/json"
	"strconv"
	"strings"

	errors "github.com/Laisky/errors/v2"
)

// Vector stores an embedding vector and supports SQL scan/value operations.
type Vector struct {
	values []float32
}

// NewVector builds a Vector from float32 values.
func NewVector(values []float32) Vector {
	cloned := make([]float32, len(values))
	copy(cloned, values)
	return Vector{values: cloned}
}

// Slice returns the underlying values as a cloned slice.
func (v Vector) Slice() []float32 {
	cloned := make([]float32, len(v.values))
	copy(cloned, v.values)
	return cloned
}

// Value converts Vector into PostgreSQL pgvector literal format.
func (v Vector) Value() (driver.Value, error) {
	if len(v.values) == 0 {
		return "[]", nil
	}

	builder := strings.Builder{}
	builder.Grow(len(v.values) * 8)
	builder.WriteByte('[')
	for idx, value := range v.values {
		if idx > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatFloat(float64(value), 'f', -1, 32))
	}
	builder.WriteByte(']')

	return builder.String(), nil
}

// Scan reads vector values from DB representations.
func (v *Vector) Scan(src any) error {
	if v == nil {
		return errors.New("vector destination is nil")
	}

	switch raw := src.(type) {
	case nil:
		v.values = nil
		return nil
	case string:
		parsed, err := parseVectorText(raw)
		if err != nil {
			return errors.WithStack(err)
		}
		v.values = parsed
		return nil
	case []byte:
		parsed, err := parseVectorText(string(raw))
		if err != nil {
			return errors.WithStack(err)
		}
		v.values = parsed
		return nil
	case []float32:
		v.values = make([]float32, len(raw))
		copy(v.values, raw)
		return nil
	case []float64:
		parsed := make([]float32, len(raw))
		for idx, item := range raw {
			parsed[idx] = float32(item)
		}
		v.values = parsed
		return nil
	default:
		return errors.Errorf("unsupported vector source type %T", src)
	}
}

// parseVectorText parses pgvector literal text into float32 values.
func parseVectorText(raw string) ([]float32, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "[]" {
		return nil, nil
	}

	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		return nil, errors.Errorf("invalid vector literal %q", raw)
	}

	var arr []float32
	if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
		return nil, errors.Wrap(err, "decode vector literal")
	}
	return arr, nil
}
