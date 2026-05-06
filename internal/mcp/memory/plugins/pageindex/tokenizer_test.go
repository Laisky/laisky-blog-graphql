package pageindex

import "testing"

func TestTokenizerCount(t *testing.T) {
	tk, err := NewTokenizer("gpt-5.4-mini")
	if err != nil {
		t.Fatalf("new tokenizer: %v", err)
	}
	if tk.Count("") != 0 {
		t.Fatalf("empty string count should be 0")
	}
	if tk.Count("Hello world") < 1 {
		t.Fatalf("'Hello world' should have at least 1 token")
	}
	ids := tk.Encode("Hello world")
	if len(ids) == 0 {
		t.Fatalf("Encode should return ids")
	}
}

func TestTokenizerFallback(t *testing.T) {
	if got := encodingForModel("custom-model-xyz"); got == "" {
		t.Fatalf("fallback encoding must not be empty")
	}
}
