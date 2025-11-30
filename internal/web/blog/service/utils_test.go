package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMarkdown2HTML(t *testing.T) {
	md := []byte("```python\na = 2\n```")
	expect := `<pre><code class="language-python">a = 2
</code></pre>`
	html := strings.TrimSpace(ParseMarkdown2HTML(md))
	require.Equal(t, expect, html, "markdown to HTML conversion failed")

	md = []byte(`<h2>abc 啊</h2>`)
	expect = `<p><h2 id="header-abcE5958A">Ⅰ、abc 啊</h2></p>`
	html = strings.TrimSpace(ParseMarkdown2HTML(md))
	require.Equal(t, expect, html, "markdown to HTML conversion failed")
}

func TestExtractMenu(t *testing.T) {
	cnt := ExtractMenu(`<h2 id="abc">abc def</h2>ffweifj<h3 id="lev 3">333</h3>j3ij23lrij`)
	expect := `<nav id="post-menu" class="h-100 flex-column align-items-stretch"><nav class="nav nav-pills flex-column"><a class="nav-link" href="#abc">abc def</a><nav class="nav nav-pills flex-column"><a class="nav-link ms-3 my-1" href="#lev 3">333</a></nav></nav></nav>`
	require.Equal(t, expect, cnt, "ExtractMenu output mismatch")
}
