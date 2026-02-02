package controller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBlogTwitterCardFormatSecurity(t *testing.T) {
	r := &QueryResolver{}
	title := `"><script>alert(1)</script>`
	imgURL := `"><img src=x onerror=alert(1)>`
	name := `foo"bar`

	output := r.blogTwitterCardFormat(title, imgURL, name)

	// After the fix, these should be correctly escaped
	assert.False(t, strings.Contains(output, title), "Output should not contain raw title")
	assert.False(t, strings.Contains(output, imgURL), "Output should not contain raw imgURL")
	assert.False(t, strings.Contains(output, name), "Output should not contain raw name")

	assert.True(t, strings.Contains(output, `content="&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;"`), "Title should be escaped")
	assert.True(t, strings.Contains(output, `content="&#34;&gt;&lt;img src=x onerror=alert(1)&gt;"`), "ImgURL should be escaped")
	assert.True(t, strings.Contains(output, `/p/foo&#34;bar/"`), "Name should be escaped")
}
