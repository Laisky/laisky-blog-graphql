package toolpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"strings"
	"testing"
)

func TestQuery(t *testing.T) {
	for _, raw := range []string{"", " \t\n", strings.Repeat("x", MaxQueryBytes+1), string([]byte{0xff})} {
		if _, err := Query(raw); err == nil {
			t.Errorf("accepted invalid query of length %d", len(raw))
		}
	}
	got, err := Query("  café 漢  ")
	if err != nil || got != "café 漢" {
		t.Fatalf("%q: %v", got, err)
	}
}
func TestOptionalIntNeverCoercesOrTruncates(t *testing.T) {
	for _, value := range []any{"3", "3garbage", "1.5", true, []int{1}, map[string]any{}, 1.5, math.NaN(), math.Inf(1), math.MaxFloat64, int64(1 << 62), float64(1 << 53), -1, 0, 21} {
		t.Run(strings.ReplaceAll(strings.TrimSpace(typeName(value)), "/", "_"), func(t *testing.T) {
			if _, err := OptionalInt(map[string]any{"top_k": value}, "top_k", 5, 1, 20); err == nil {
				t.Fatalf("accepted %v", value)
			}
		})
	}
	for _, value := range []any{1, int32(2), int64(3), float64(4), float32(5), json.Number("6"), json.Number("7e0")} {
		if _, err := OptionalInt(map[string]any{"top_k": value}, "top_k", 5, 1, 20); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range []map[string]any{nil, {}, {"top_k": nil}} {
		n, err := OptionalInt(args, "top_k", 5, 1, 20)
		if err != nil || n != 5 {
			t.Fatalf("%v %v", n, err)
		}
	}
}
func typeName(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "non-finite"
	}
	return string(b)
}
func TestOptionalBool(t *testing.T) {
	for _, value := range []any{"false", 0, 1, []bool{false}} {
		if _, err := OptionalBool(map[string]any{"output_markdown": value}, "output_markdown", true); err == nil {
			t.Fatalf("accepted %v", value)
		}
	}
	for _, value := range []any{nil, true, false} {
		got, err := OptionalBool(map[string]any{"output_markdown": value}, "output_markdown", true)
		if err != nil || got != (value != false) {
			t.Fatalf("%v %v", got, err)
		}
	}
}
func TestURLAdmission(t *testing.T) {
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	for _, raw := range []string{"https://example.com/a?q=1", "http://8.8.8.8/", "https://[2606:4700:4700::1111]/", " HTTPS://example.com "} {
		if err := validateFetchURL(context.Background(), raw, lookup); err != nil {
			t.Errorf("rejected %q: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"file:///etc/passwd",
		"gopher://example.com",
		"http:///x",
		"http://localhost",
		"http://foo.localhost/",
		"http://service.local/",
		"http://metadata.google.internal",
		"http://user:secret@example.com/",
		"http://127.0.0.1",
		"http://10.1.2.3",
		"http://169.254.169.254",
		"http://100.64.0.1",
		"http://0.0.0.0",
		"http://224.0.0.1",
		"http://[::1]",
		"http://[::ffff:127.0.0.1]",
		"http://[fe80::1%25eth0]",
		"http://2130706433",
		"http://0177.0.0.1",
		"http://0x7f000001",
		"https://example.com:0",
		"https://example.com:99999",
	} {
		if err := validateFetchURL(context.Background(), raw, lookup); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, addresses := range [][]net.IPAddr{nil, {{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("192.168.0.1")}}, {{IP: []byte{1}}}} {
		if err := validateFetchURL(context.Background(), "https://example.test", func(context.Context, string) ([]net.IPAddr, error) { return addresses, nil }); err == nil {
			t.Fatal("accepted empty/unsafe DNS")
		}
	}
}
func TestURLCancellationAndDNSFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := validateFetchURL(ctx, "https://example.com", func(context.Context, string) ([]net.IPAddr, error) { called = true; return nil, nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("%v called=%v", err, called)
	}
	err = validateFetchURL(context.Background(), "https://example.com?secret=hidden", func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("secret DNS details") })
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error %v", err)
	}
}
func TestURLLogRedaction(t *testing.T) {
	for _, raw := range []string{"https://user:secret@example.com/a?secret=value#secret", "http://secret@bad%host", "https://example.com/a?secret", "user:secret", "//secret/path", "http:secret"} {
		if strings.Contains(URLForLog(raw), "secret") {
			t.Fatalf("leaked credentials: %s", URLForLog(raw))
		}
	}
}
