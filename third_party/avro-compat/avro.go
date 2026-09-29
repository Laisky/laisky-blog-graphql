// Package avro adapts goar's legacy tag-codec API to the fixed v2 implementation.
// It contains no v1 decoder code. Keep this surface minimal so unexpected new
// upstream API usage fails compilation rather than silently selecting old code.
package avro

import avrov2 "github.com/hamba/avro/v2"

// Schema is the v2 schema used by the compatibility entry points.
type Schema = avrov2.Schema

// Parse delegates schema parsing to avro v2.
func Parse(schema string) (Schema, error) { return avrov2.Parse(schema) }

// Marshal delegates binary encoding to avro v2.
func Marshal(schema Schema, value any) ([]byte, error) { return avrov2.Marshal(schema, value) }

// Unmarshal delegates binary decoding to avro v2, including its malformed-input fixes.
func Unmarshal(schema Schema, data []byte, value any) error { return avrov2.Unmarshal(schema, data, value) }
