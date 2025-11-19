package rag

import "testing"

func TestTokenize(t *testing.T) {
	tokens := tokenize("Hello, HELLO world!! world")
	if len(tokens) != 2 {
		t.Fatalf("expected 2 unique tokens, got %d", len(tokens))
	}
	if tokens[0] != "hello" {
		t.Fatalf("unexpected token order")
	}
}
