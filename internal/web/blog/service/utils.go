package service

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	gutils "github.com/Laisky/go-utils/v6"
	"github.com/Laisky/zap"
	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

var (
	titleRegexp     = regexp.MustCompile(`<(h[23])[^>]{0,}>([^<]+)</\w+>`)
	titleMenuRegexp = regexp.MustCompile(`<(h[23]) *id="([^"]*)">([^<]+)</\w+>`) // extract menu
	validHtmlId     = regexp.MustCompile(`[^a-zA-Z0-9\-_]`)
	httpcli         *http.Client
)

func init() {
	var err error
	if httpcli, err = gutils.NewHTTPClient(
		gutils.WithHTTPClientTimeout(20 * time.Second),
	); err != nil {
		log.Logger.Panic("init httpcli", zap.Error(err))
	}
}

// ParseMarkdown2HTML parse markdown to string
func ParseMarkdown2HTML(md []byte) (cnt string) {
	htmlFlags := html.CommonFlags | html.HrefTargetBlank
	opts := html.RendererOptions{Flags: htmlFlags}
	renderer := html.NewRenderer(opts)
	cnt = string(markdown.ToHTML(md, nil, renderer))
	cnt = titleRegexp.ReplaceAllString(cnt, `<$1 id="$2">$2</$1>`)
	cnt = strings.ReplaceAll(cnt, `class="codehilite"`, `class="codehilite highlight"`)
	var (
		tl, tlev, tid, ttext string
		l2cnt, l3cnt         int
	)
	for _, ts := range titleMenuRegexp.FindAllStringSubmatch(cnt, -1) {
		tl = ts[0]
		tlev = strings.ToLower(ts[1])
		tid = ts[2]
		ttext = ts[3]
		switch tlev {
		case "h2":
			l3cnt = 0
			l2cnt++
			ttext = gutils.Number2Roman(l2cnt) + "、" + ttext
		case "h3":
			l3cnt++
			ttext = strconv.FormatInt(int64(l3cnt), 10) + "、" + ttext
		default:
			log.Logger.Error("unknown title level", zap.String("lev", tlev))
		}

		tid = convertTitleID(tid)
		cnt = strings.ReplaceAll(cnt, tl, `<`+tlev+` id="`+tid+`">`+ttext+`</`+tlev+`>`)
	}
	return cnt
}

// convertTitleID convert title to valid html id
//
// https://www.w3.org/TR/REC-html40/types.html#:~:text=ID%20and%20NAME%20tokens%20must,periods%20(%22.%22).
func convertTitleID(title string) string {
	return "header-" + validHtmlId.ReplaceAllString(url.QueryEscape(title), "")
}

// Truncate truncate string to n runes
func Truncate(s string, n int) string {
	if n <= 0 {
		return s
	}

	var count int
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}

	return s
}

func ExtractMenu(html string) string {
	var (
		menu                 = `<nav id="post-menu" class="h-100 flex-column align-items-stretch"><nav class="nav nav-pills flex-column">`
		level, escapedTl, tl string
		l2cnt, l3cnt         string
	)
	for _, ts := range titleMenuRegexp.FindAllStringSubmatch(html, -1) {
		level = strings.ToLower(ts[1])
		escapedTl = ts[2]
		tl = ts[3]
		if level == "h2" {
			if l2cnt != "" {
				if l3cnt != "" {
					l2cnt += l3cnt + `</nav>`
				}
				menu += l2cnt
			}
			l3cnt = ""
			l2cnt = `<a class="nav-link" href="#` + escapedTl + `">` + tl + `</a>`
		} else if level == "h3" {
			if l3cnt == "" {
				l3cnt = `<nav class="nav nav-pills flex-column"><a class="nav-link ms-3 my-1" href="#` + escapedTl + `">` + tl + `</a>`
			} else {
				l3cnt += `<a class="nav-link ms-3 my-1" href="#` + escapedTl + `">` + tl + `</a>`
			}
		}
	}

	if l3cnt != "" {
		l2cnt += l3cnt + `</nav>`
	}
	menu += l2cnt + `</nav></nav>`
	return menu
}
