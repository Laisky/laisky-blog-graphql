package service

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Laisky/laisky-blog-graphql/library/log"

	"github.com/Laisky/zap"
	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"
)

var (
	titleRegexp     = regexp.MustCompile(`<(h[23])[^>]{0,}>([^<]+)</\w+>`)
	titleMenuRegexp = regexp.MustCompile(`<(h[23]) *id="([^"]*)">([^<]+)</\w+>`) // extract menu
	chineseSerial   = map[string]string{
		"0": "\u3007",
		"1": "\u4E00",
		"2": "\u4E8C",
		"3": "\u4E09",
		"4": "\u56DB",
		"5": "\u4E94",
		"6": "\u516D",
		"7": "\u4E03",
		"8": "\u516B",
		"9": "\u4E5D",
	}
)

func num2CN(num int) string {
	r := ""
	for _, v := range strconv.FormatInt(int64(num), 10) {
		r += chineseSerial[string(v)]
	}

	return r
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
			l2cnt++
			l3cnt = 0
			ttext = num2CN(l2cnt) + "、" + ttext
		case "h3":
			l3cnt++
			ttext = strconv.FormatInt(int64(l3cnt), 10) + "、" + ttext
		default:
			log.Logger.Error("unknown title level", zap.String("lev", tlev))
		}

		tid = url.QueryEscape(tid)
		cnt = strings.ReplaceAll(cnt, tl, `<`+tlev+` id="`+tid+`">`+ttext+`</`+tlev+`>`)
	}
	return cnt
}

func ExtractMenu(html string) string {
	var (
		menu                 = `<ul class="nav" role="tablist">`
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
					l2cnt += l3cnt + `</ul></li>`
				} else {
					l2cnt += `</li>`
				}
				menu += l2cnt
			}
			l3cnt = ""
			l2cnt = `<li><a href="#` + escapedTl + `">` + tl + `</a>`
		} else if level == "h3" {
			if l3cnt == "" {
				l3cnt = `<ul class="nav"><li><a href="#` + escapedTl + `">` + tl + `</a></li>`
			} else {
				l3cnt += `<li><a href="#` + escapedTl + `">` + tl + `</a></li>`
			}
		}
	}

	if l3cnt != "" {
		l2cnt += l3cnt + `</ul></li>`
	} else {
		l2cnt += `</li>`
	}
	menu += l2cnt + `</ul>`
	return menu
}
