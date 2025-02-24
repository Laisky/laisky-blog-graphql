package model

import (
	"testing"

	"github.com/Laisky/go-utils/v5/json"
	"github.com/stretchr/testify/require"
)

func TestXxx(t *testing.T) {
	type a struct {
		A, B, C string
	}

	type b struct {
		a
		A int
	}

	v := b{
		A: 123,
		a: a{
			A: "A",
			B: "B",
			C: "C",
		},
	}

	data, err := json.MarshalToString(v)
	require.NoError(t, err)
	t.Log(data)
	t.Error()
}
