// Package avro adapts goar's legacy tag-codec API to the maintained v2 implementation.
// It contains no hamba decoder code. Keep this surface minimal so unexpected new
// upstream API usage fails compilation rather than silently selecting old code.
package avro

import (
	"errors"
	"fmt"
	"io"

	avrov2 "github.com/iskorotkov/avro/v2"
)

// Schema is the maintained v2 schema used by the compatibility entry points.
type Schema = avrov2.Schema

// The legacy encoder used element-count headers. Keep that wire representation,
// and bound collection allocations independently of attacker-controlled counts.
var codec = avrov2.Config{
	DisableBlockSizeHeader: true,
	MaxSliceAllocSize:      16 << 20,
	MaxMapAllocSize:        16 << 20,
}.Freeze()

// Parse delegates schema parsing to avro v2.
func Parse(schema string) (Schema, error) { return avrov2.Parse(schema) }

// Marshal retains legacy block headers while using the maintained encoder.
func Marshal(schema Schema, value any) ([]byte, error) { return codec.Marshal(schema, value) }

// Unmarshal decodes one complete value and rejects truncated or trailing data.
// goar represents an empty tag list with zero bytes, which remains supported.
func Unmarshal(schema Schema, data []byte, value any) error {
	if len(data) == 0 {
		return codec.Unmarshal(schema, data, value)
	}
	// The convenience Unmarshal API treats EOF as success, including an EOF in
	// a truncated collection. Inspect Reader.Error before testing for extra data.
	reader := avrov2.NewReader(nil, 1024, avrov2.WithReaderConfig(codec)).Reset(data)
	reader.ReadVal(schema, value)
	if reader.Error != nil {
		return fmt.Errorf("decode legacy avro: %w", reader.Error)
	}
	reader.Peek()
	if reader.Error == nil {
		return errors.New("trailing data after legacy avro value")
	}
	if !errors.Is(reader.Error, io.EOF) {
		return fmt.Errorf("finish legacy avro value: %w", reader.Error)
	}
	return nil
}
