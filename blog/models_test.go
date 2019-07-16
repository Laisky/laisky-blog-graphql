package blog_test

import (
	"strings"
	"testing"

	"github.com/Laisky/laisky-blog-graphql/blog"
)

func TestParseMarkdown2HTML(t *testing.T) {
	md := []byte("```python\na = 2\n```")
	expect := `<pre><code class="language-python">a = 2
</code></pre>`
	html := strings.TrimSpace(blog.ParseMarkdown2HTML(md))
	if html != expect {
		t.Errorf("got: `%v`", string(html))
		t.Fatalf("expect: `%v`", expect)
	}

	md = []byte(`<h2>abc 啊</h2>`)
	expect = `<p><h2 id="abc+%E5%95%8A">一、abc 啊</h2></p>`
	html = strings.TrimSpace(blog.ParseMarkdown2HTML(md))
	if html != expect {
		t.Errorf("got: `%v`", string(html))
		t.Fatalf("expect: `%v`", expect)
	}
}

func TestExtractMenu(t *testing.T) {
	cnt := blog.ExtractMenu(`<h2 id="abc">abc def</h2>ffweifj<h3 id="lev 3">333</h3>j3ij23lrij`)
	expect := `<ul class="nav affix-top" data-spy="affix"><li><a href="#abc">abc def</a><ul class="nav"><li><a href="#lev 3">333</a></li></ul></li></ul>`
	if cnt != expect {
		t.Errorf("got: `%v`", cnt)
		t.Fatalf("expect: `%v`", expect)
	}
}
