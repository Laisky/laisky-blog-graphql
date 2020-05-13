package libs

import (
	jsoniter "github.com/json-iterator/go"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

func appendQuote(v []byte) []byte {
	r := []byte("\"")
	r = append(r, v...)
	r = append(r, '"')
	return r
}
