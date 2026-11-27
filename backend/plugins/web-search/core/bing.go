package websearch

import (
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf16"

	"golang.org/x/net/html"
)

var (
	errInvalidHTML = errors.New("invalid_response")
	errHTMLLimit   = errors.New("parse_limit")
	htmlChallenge  = regexp.MustCompile(`(?i)captcha|challenge|anomaly`)
	challengeClass = regexp.MustCompile(`(?i)g-recaptcha|h-captcha|cf-challenge|anomaly-modal`)
	challengeTitle = regexp.MustCompile(`(?i)captcha|access denied|just a moment`)
	emptyBingPage  = regexp.MustCompile(`(?i)no results|did not match any documents|no more results|没有找到|未找到|找不到相关`)
	bingBase       = url.URL{Scheme: "https", Host: "www.bing.com"}
)

// parseBing reads inert HTML. Neither parsing nor decoding a citation wrapper
// makes another request. Structural limits match the TypeScript adapter.
func parseBing(body string) ([]Result, error) {
	root := &html.Node{Type: html.DocumentNode}
	stack := []*html.Node{root}
	z := html.NewTokenizer(strings.NewReader(body))
	hasPage, elements := false, 0
	var cards, titles []*html.Node
	for {
		kind := z.Next()
		raw := z.Raw()
		if kind == html.ErrorToken {
			if z.Err() != io.EOF || len(raw) != 0 || !hasPage {
				return nil, errInvalidHTML
			}
			break
		}
		if kind == html.CommentToken {
			if strings.HasPrefix(string(raw), "<!--") && !strings.HasSuffix(string(raw), "-->") {
				return nil, errInvalidHTML
			}
			continue
		}
		if kind != html.TextToken && len(raw) > 8193 && len(utf16.Encode([]rune(string(raw)))) > 8193 {
			return nil, errInvalidHTML
		}
		parent := stack[len(stack)-1]
		if kind == html.TextToken {
			if !ignoreHTMLText(parent.Data) {
				parent.AppendChild(&html.Node{Type: html.TextNode, Data: string(z.Text())})
			}
			continue
		}
		if kind == html.DoctypeToken {
			continue
		}
		token := z.Token()
		if ignoreHTMLText(parent.Data) && !(kind == html.EndTagToken && token.Data == parent.Data) {
			continue
		}
		if kind == html.EndTagToken {
			for i := len(stack) - 1; i > 0; i-- {
				if stack[i].Data == token.Data {
					stack = stack[:i]
					break
				}
			}
			continue
		}
		if elements == 30_000 || len(stack) == 128 {
			return nil, errHTMLLimit
		}
		elements++
		switch token.Data {
		case "html", "body", "div", "ol":
			hasPage = true
		}
		node := &html.Node{Type: html.ElementNode, Data: token.Data, Attr: token.Attr}
		if challengeClass.MatchString(htmlAttr(node, "id")) || challengeClass.MatchString(htmlAttr(node, "class")) {
			return nil, errInvalidHTML
		}
		if node.Data == "form" || node.Data == "iframe" {
			for _, attr := range node.Attr {
				if htmlChallenge.MatchString(attr.Val) {
					return nil, errInvalidHTML
				}
			}
		}
		parent.AppendChild(node)
		if node.Data == "li" && len(cards) < 100 && htmlClass(node, "b_algo") {
			cards = append(cards, node)
		} else if node.Data == "title" {
			titles = append(titles, node)
		}
		if kind == html.StartTagToken && !htmlVoid(node.Data) {
			stack = append(stack, node)
		}
	}
	for _, title := range titles {
		if challengeTitle.MatchString(htmlText(title)) {
			return nil, errInvalidHTML
		}
	}
	items := make([]Result, 0, len(cards))
	for _, card := range cards {
		heading := htmlFind(card, "h2", "")
		if heading == nil {
			continue
		}
		anchor := htmlFind(heading, "a", "")
		if anchor == nil {
			continue
		}
		target := bingTarget(htmlAttr(anchor, "href"))
		title := htmlText(heading)
		if target == "" || title == "" {
			continue
		}
		snippet := htmlFind(card, "", "b_caption")
		if snippet == nil {
			snippet = htmlFind(card, "p", "")
		}
		items = append(items, Result{"bing", slice(title, 512), target, slice(htmlText(snippet), 4000)})
	}
	if len(items) == 0 && !emptyBingPage.MatchString(htmlText(root)) {
		return nil, errInvalidHTML
	}
	return Unique(items, 100), nil
}

func bingTarget(raw string) string {
	target, err := bingBase.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(target.Hostname(), "."))
	if (host == "www.bing.com" || host == "cn.bing.com") && target.Path == "/ck/a" {
		encoded, ok := strings.CutPrefix(target.Query().Get("u"), "a1")
		if !ok {
			return ""
		}
		value, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(encoded, "="))
		if err != nil {
			return ""
		}
		return PublicURL(string(value))
	}
	for _, domain := range []string{"bing.com", "google.com", "duckduckgo.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return ""
		}
	}
	return PublicURL(target.String())
}

func htmlAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func htmlClass(n *html.Node, class string) bool {
	for field := range strings.FieldsSeq(htmlAttr(n, "class")) {
		if field == class {
			return true
		}
	}
	return false
}

func htmlFind(root *html.Node, tag, class string) *html.Node {
	for n := root.FirstChild; n != nil; n = n.NextSibling {
		if n.Type != html.ElementNode {
			continue
		}
		if (tag == "" || n.Data == tag) && (class == "" || htmlClass(n, class)) {
			return n
		}
		if found := htmlFind(n, tag, class); found != nil {
			return found
		}
	}
	return nil
}

func htmlText(n *html.Node) string {
	var out strings.Builder
	var appendText func(*html.Node)
	appendText = func(n *html.Node) {
		if n.Type == html.TextNode {
			for word := range strings.FieldsFuncSeq(n.Data, querySpace) {
				if out.Len() != 0 {
					out.WriteByte(' ')
				}
				out.WriteString(word)
			}
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			appendText(child)
		}
	}
	if n != nil {
		appendText(n)
	}
	return out.String()
}

func ignoreHTMLText(tag string) bool {
	switch tag {
	case "script", "style", "noscript", "svg":
		return true
	}
	return false
}

func htmlVoid(tag string) bool {
	switch tag {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	}
	return false
}
