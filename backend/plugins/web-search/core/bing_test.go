package websearch_test

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	websearch "github.com/killerprojecte/HumanEngine/backend/plugins/web-search/core"
)

func TestBingHTMLSourceFixtures(t *testing.T) {
	card := func(href, title, tail string) string {
		return `<li class="b_algo"><h2><a href="` + href + `">` + title + `</a></h2>` + tail + `</li>`
	}
	article := "https://example.com/story"
	wrapped := "https://www.bing.com/ck/a?u=a1" + base64.RawURLEncoding.EncodeToString([]byte(article))
	private := "/ck/a?u=a1" + base64.RawURLEncoding.EncodeToString([]byte("http://127.0.0.1/"))
	for _, tc := range []struct {
		name, html, status string
		want               []websearch.Result
	}{
		{"citation", `<html><ol>` + card("https://example.com/a", "One &amp; Two", `<div class="b_caption"><p>Summary</p></div>`) + `</ol></html>`, "ok",
			[]websearch.Result{{Provider: "bing", Title: "One & Two", URL: "https://example.com/a", Snippet: "Summary"}}},
		{"wrapper-and-private", `<html>` + card(wrapped, "Story<script>ignore()</script>", "") + card(private, "Private", "") + card(article, "Duplicate", "") + `</html>`, "ok",
			[]websearch.Result{{Provider: "bing", Title: "Story", URL: article}}},
		{"empty", `<html><body>No results found.</body></html>`, "no_results", []websearch.Result{}},
		{"title-challenge", `<html><title>Just a moment</title>` + card(article, "Not evidence", "") + "</html>", "unavailable", []websearch.Result{}},
		{"form-challenge", `<html><form action="/anomaly.js">captcha</form></html>`, "unavailable", []websearch.Result{}},
		{"unsafe-only", `<html>` + card(private, "Private", "") + `</html>`, "unavailable", []websearch.Result{}},
		{"unterminated", `<html>` + card(article, "Not evidence", "") + `<!--`, "unavailable", []websearch.Result{}},
		{"overlong", `<html><div ` + strings.Repeat("a", 65536) + `></div></html>`, "unavailable", []websearch.Result{}},
		{"depth", `<html>` + strings.Repeat("<div>", 130), "partial", []websearch.Result{}},
		{"nodes", `<html>` + strings.Repeat("<br>", 30_001), "partial", []websearch.Result{}},
		{"beyond-1mib", `<html><div>` + strings.Repeat("x", 1048577) + `</div>` + card(article, "Large page", ""), "ok",
			[]websearch.Result{{Provider: "bing", Title: "Large page", URL: article}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := websearch.FinishSearch(websearch.SearchObservation{Provider: "bing", Limit: 3, Text: tc.html,
				Body: json.RawMessage(`{"resultId":"body"}`), Status: 200, Budget: 2097152, Attempted: []string{"bing"}})
			if got.Status != tc.status || !reflect.DeepEqual(got.Results, tc.want) || len(got.Body) == 0 ||
				got.BodyProvider != "bing" || got.ParseLimited == nil || *got.ParseLimited != (tc.status == "partial") {
				t.Fatalf("wrong Bing result: %+v", got)
			}
		})
	}
}
