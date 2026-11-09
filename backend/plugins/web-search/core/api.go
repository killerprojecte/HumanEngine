// Package websearch contains provider requests and result policy executed by WASM.
package websearch

import (
	"encoding/json"
	"errors"
	resources "github.com/killerprojecte/HumanEngine/backend/plugins/web-search"
	"golang.org/x/net/idna"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf16"
)

type Request struct {
	URL    string `json:"url"`
	Format string `json:"format"`
	Init   Init   `json:"init"`
}
type Init struct {
	Method   string            `json:"method"`
	Headers  map[string]string `json:"headers"`
	Body     string            `json:"body,omitempty"`
	Redirect string            `json:"redirect,omitempty"`
}
type Result struct {
	Provider string `json:"provider"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Snippet  string `json:"snippet"`
}

func BuildAPI(provider, query string, limit int, key string) (Request, error) {
	if key == "" {
		return Request{}, errors.New("credentials_missing")
	}
	request := Request{Format: "json", Init: Init{Method: "POST", Headers: map[string]string{"Content-Type": "application/json"}}}
	var body any
	switch provider {
	case "tavily":
		request.URL = "https://api.tavily.com/search"
		request.Init.Headers["Authorization"] = "Bearer " + key
		body = map[string]any{"query": query, "max_results": limit, "search_depth": "basic", "include_answer": false, "include_raw_content": false, "auto_parameters": false}
	case "deepseek":
		request.URL = "https://api.deepseek.com/v1/tools/search"
		request.Init.Headers["Authorization"] = "Bearer " + key
		body = map[string]any{"query": query, "count": limit, "need_content": false}
	case "you", "you_news":
		request.URL = "https://ydc-index.io/v1/search"
		request.Init.Headers["X-API-Key"] = key
		body = map[string]any{"query": query, "count": limit}
	default:
		return Request{}, errors.New("provider_disabled")
	}
	encoded, err := json.Marshal(body)
	request.Init.Body = string(encoded)
	return request, err
}
func BuildHTML(provider, query string) (Request, error) {
	endpoints := map[string]string{"bing": "https://www.bing.com/search", "google": "https://www.google.com/search", "sogou": "https://www.sogou.com/web", "duckduckgo": "https://html.duckduckgo.com/html/"}
	endpoint := endpoints[provider]
	if endpoint == "" {
		return Request{}, errors.New("provider_disabled")
	}
	address, _ := url.Parse(endpoint)
	values := address.Query()
	key := "q"
	if provider == "sogou" {
		key = "query"
	}
	values.Set(key, query)
	address.RawQuery = values.Encode()
	return Request{URL: address.String(), Format: "html", Init: Init{Method: "GET", Redirect: "error", Headers: map[string]string{"Accept": "text/html", "User-Agent": "HumanEngine-WebSearch/1.0"}}}, nil
}

var blockedHost = regexp.MustCompile(`(?:^|\.)(localhost|local|internal|test|invalid)$`)
var numericLabel = regexp.MustCompile(`(?i)^(?:[0-9]+|0x[0-9a-f]+)$`)

func PublicURL(raw string) string {
	if len(utf16.Encode([]rune(raw))) > 4096 {
		return ""
	}
	address, err := url.Parse(raw)
	if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.User != nil {
		return ""
	}
	host, err := idna.Lookup.ToASCII(strings.ToLower(address.Hostname()))
	if err != nil {
		return ""
	}
	normalized := strings.TrimSuffix(host, ".")
	port := address.Port()
	if (address.Scheme == "http" && port == "80") || (address.Scheme == "https" && port == "443") {
		port = ""
	}
	labels := strings.Split(normalized, ".")
	if port != "" || !strings.Contains(normalized, ".") || net.ParseIP(normalized) != nil || numericLabel.MatchString(labels[len(labels)-1]) || blockedHost.MatchString(normalized) {
		return ""
	}
	address.Host = host
	if address.Path == "" {
		address.Path = "/"
	}
	base := &url.URL{}
	return base.ResolveReference(address).String()
}
func slice(value string, n int) string {
	units := utf16.Encode([]rune(value))
	if len(units) > n {
		units = units[:n]
	}
	return string(utf16.Decode(units))
}
func result(provider string, row map[string]any, snippet any) (Result, bool) {
	title, ok := row["title"].(string)
	rawURL, _ := row["url"].(string)
	href := PublicURL(rawURL)
	if !ok || strings.TrimSpace(title) == "" || href == "" {
		return Result{}, false
	}
	text, _ := snippet.(string)
	return Result{provider, slice(title, 512), href, slice(text, 4000)}, true
}
func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func ParseAPI(provider, body string) ([]Result, error) {
	var data any
	if json.Unmarshal([]byte(body), &data) != nil {
		return nil, errors.New("invalid_response")
	}
	value := object(data)
	entries := value["results"]
	if provider == "you" {
		entries = object(entries)["web"]
	} else if provider == "you_news" {
		container := object(entries)
		entries = container["news"]
		if entries == nil {
			if _, ok := container["web"].([]any); ok {
				entries = []any{}
			}
		}
	}
	array, ok := entries.([]any)
	if !ok {
		return nil, errors.New("invalid_response")
	}
	results := []Result{}
	for _, entry := range array {
		row := object(entry)
		var snippet any
		switch provider {
		case "tavily":
			snippet = row["content"]
		case "you":
			if snippets, ok := row["snippets"].([]any); ok {
				parts := []string{}
				for _, v := range snippets {
					if s, ok := v.(string); ok {
						parts = append(parts, s)
						if len(parts) == 5 {
							break
						}
					}
				}
				snippet = strings.Join(parts, "\n")
			} else {
				snippet = row["description"]
			}
		case "you_news":
			snippet = row["description"]
		default:
			snippet = row["snippet"]
		}
		if item, ok := result(provider, row, snippet); ok {
			results = append(results, item)
		}
	}
	if len(array) > 0 && len(results) == 0 {
		return nil, errors.New("invalid_response")
	}
	return results, nil
}
func Unique(items []Result, limit int) []Result {
	seen := map[string]bool{}
	out := []Result{}
	for _, item := range items {
		if !seen[item.URL] {
			seen[item.URL] = true
			out = append(out, item)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}
func Invoke(input []byte) any {
	var in struct {
		Operation, Provider, Query, Key, Body string
		Limit                                 int
	}
	if json.Unmarshal(input, &in) != nil {
		return map[string]any{"error": "invalid_plugin_input"}
	}
	var value any
	var err error
	switch in.Operation {
	case "manifest":
		return resources.Manifest()
	case "build_api":
		value, err = BuildAPI(in.Provider, in.Query, in.Limit, in.Key)
	case "build_html":
		value, err = BuildHTML(in.Provider, in.Query)
	case "parse_api":
		value, err = ParseAPI(in.Provider, in.Body)
	default:
		err = errors.New("unknown_plugin_operation")
	}
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return value
}
