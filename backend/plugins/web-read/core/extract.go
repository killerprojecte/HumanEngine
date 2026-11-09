// Package webread implements inert document extraction inside the V2 artifact.
package webread

import (
	"encoding/json"
	resources "github.com/killerprojecte/HumanEngine/backend/plugins/web-read"
	"golang.org/x/net/html"
	"regexp"
	"strings"
	"unicode/utf16"
)

var skipped = map[string]bool{"script": true, "style": true, "template": true, "svg": true, "canvas": true, "iframe": true, "object": true, "embed": true}
var blocks = map[string]bool{"p": true, "div": true, "section": true, "article": true, "main": true, "header": true, "footer": true, "nav": true, "li": true, "br": true, "hr": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "tr": true, "pre": true, "blockquote": true}
var controls = regexp.MustCompile("[\\x00-\\x08\\x0b\\x0c\\x0e-\\x1f\\x7f]")
var spaces = regexp.MustCompile(`[ \t\r\f]+`)
var aroundLines = regexp.MustCompile(` *\n *`)
var manyLines = regexp.MustCompile(`\n{3,}`)

func clean(s string) string {
	return strings.TrimSpace(manyLines.ReplaceAllString(aroundLines.ReplaceAllString(spaces.ReplaceAllString(controls.ReplaceAllString(s, ""), " "), "\n"), "\n\n"))
}

type Text struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

func Extract(markup string) (Text, error) {
	// The legacy inert parser preserves lexical nesting; an HTML5 browser parser
	// repairs malformed head/paragraph markup and changes the extracted text.
	type frame struct {
		name                             string
		title, hidden, suppressed, block bool
	}
	stack := []frame{{}}
	var output, titles []string
	void := map[string]bool{"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true, "link": true, "meta": true, "param": true, "source": true, "track": true, "wbr": true}
	closeTop := func() {
		last := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if last.block && !last.suppressed {
			output = append(output, "\n")
		}
	}
	tokenizer := html.NewTokenizer(strings.NewReader(markup))
	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			break
		}
		token := tokenizer.Token()
		parent := stack[len(stack)-1]
		switch tokenType {
		case html.TextToken:
			if !parent.suppressed {
				if parent.title {
					titles = append(titles, token.Data)
				} else if !parent.hidden {
					output = append(output, token.Data)
				}
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name := strings.ToLower(token.Data)
			suppressed := parent.suppressed || skipped[name]
			for _, attr := range token.Attr {
				if attr.Key == "hidden" || (attr.Key == "aria-hidden" && attr.Val == "true") {
					suppressed = true
				}
			}
			next := frame{name: name, title: parent.title || name == "title", hidden: parent.hidden || name == "head", suppressed: suppressed, block: blocks[name]}
			if next.block && !suppressed {
				output = append(output, "\n")
			}
			stack = append(stack, next)
			if tokenType == html.SelfClosingTagToken || void[name] {
				closeTop()
			}
		case html.EndTagToken:
			name := strings.ToLower(token.Data)
			index := -1
			for i := len(stack) - 1; i > 0; i-- {
				if stack[i].name == name {
					index = i
					break
				}
			}
			if index >= 0 {
				for len(stack) > index {
					closeTop()
				}
			}
		}
	}
	for len(stack) > 1 {
		closeTop()
	}
	title := utf16.Encode([]rune(clean(strings.Join(titles, " "))))
	if len(title) > 512 {
		title = title[:512]
	}
	return Text{Title: string(utf16.Decode(title)), Text: clean(strings.Join(output, ""))}, nil
}
func Invoke(input []byte) any {
	var in struct {
		Operation string `json:"operation"`
		HTML      string `json:"html"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return map[string]any{"error": "invalid_plugin_input"}
	}
	switch in.Operation {
	case "networkHeaders", "networkPrepare", "networkChoose", "networkRedirect":
		return runtimeInvoke(input, in.Operation)
	case "registrations", "plan", "cachePolicy", "response", "error":
		return runtimeInvoke(input, in.Operation)
	case "manifest":
		return resources.Manifest()
	case "extract":
		text, err := Extract(in.HTML)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return text
	default:
		return map[string]any{"error": "unknown_plugin_operation"}
	}
}
