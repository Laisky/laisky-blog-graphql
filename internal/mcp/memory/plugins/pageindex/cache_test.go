package pageindex

import (
	"path/filepath"
	"testing"
)

func TestCachePutGet(t *testing.T) {
	dir := t.TempDir()
	c, err := NewCache(CacheConfig{Enabled: true, Path: filepath.Join(dir, "c.bbolt")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	key := CacheKey("m", "prompt", []byte("schema"), []byte("params"))
	if err := c.Put(key, &Response{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.Get(key)
	if err != nil || !ok {
		t.Fatalf("get failed ok=%v err=%v", ok, err)
	}
	if got.Text != "hi" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestNoopCache(t *testing.T) {
	c, err := NewCache(CacheConfig{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	key := CacheKey("m", "p", nil, nil)
	if err := c.Put(key, &Response{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.Get(key); ok {
		t.Fatal("noop cache should never hit")
	}
}
