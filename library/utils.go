// Package library contains helper functions
package library

func appendQuote(v []byte) []byte {
	r := []byte("\"")
	r = append(r, v...)
	r = append(r, '"')
	return r
}
