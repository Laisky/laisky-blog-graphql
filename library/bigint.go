package library

import (
	"encoding/json"
	"io"
	"math"
	"strconv"

	"github.com/Laisky/errors/v2"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// BigInt is a signed 64-bit integer transported as a decimal string.
//
// GraphQL's built-in Int is defined as 32-bit, which cannot carry file sizes,
// byte offsets or database BIGINT identifiers. Numbers also lose precision in
// clients whose JSON numbers are IEEE-754 doubles, so BigInt marshals as a
// quoted decimal string and rejects anything that is not an exact integer.
type BigInt int64

// Int64 returns the underlying value.
func (b BigInt) Int64() int64 { return int64(b) }

// NewBigInt builds a BigInt from an int64.
func NewBigInt(value int64) BigInt { return BigInt(value) }

// UnmarshalGQL implements the graphql.Unmarshaler interface. It accepts a
// decimal string, a JSON integer or an integral JSON number, and rejects
// fractional, non-finite and out-of-range input rather than truncating it.
func (b *BigInt) UnmarshalGQL(vi any) error {
	switch v := vi.(type) {
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return errors.Wrapf(err, "BigInt must be a decimal string within int64, got %q", v)
		}
		*b = BigInt(parsed)
		return nil
	case int:
		*b = BigInt(v)
		return nil
	case int32:
		*b = BigInt(v)
		return nil
	case int64:
		*b = BigInt(v)
		return nil
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return errors.Wrapf(err, "BigInt must be an integer within int64, got %q", v.String())
		}
		*b = BigInt(parsed)
		return nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || v < math.MinInt64 || v >= math.MaxInt64 {
			return errors.Errorf("BigInt must be an exact integer within int64, got %v", v)
		}
		*b = BigInt(int64(v))
		return nil
	default:
		return errors.Errorf("BigInt must be a decimal string or integer, got %T", vi)
	}
}

// MarshalGQL implements the graphql.Marshaler interface, writing a quoted
// decimal string so no client rounds a 64-bit value.
func (b BigInt) MarshalGQL(w io.Writer) {
	if _, err := w.Write(appendQuote([]byte(strconv.FormatInt(int64(b), 10)))); err != nil {
		log.Logger.Error("write bigint bytes", zap.Error(err))
	}
}
