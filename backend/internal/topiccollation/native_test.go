//go:build humanengine_icu && cgo

package topiccollation

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestPinnedNodeUnicodeOracle(t *testing.T) {
	raw, err := os.ReadFile("testdata/node.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Node, ICU, CLDR, Unicode string
		Pairs                    [][2]string
		Oracle                   []struct {
			Locale   string
			Resolved struct {
				Locale, Sensitivity, Usage, CaseFirst string
				Numeric, IgnorePunctuation            bool
			}
			Results []int
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Node != SourceNode || fixture.ICU != "78.3" || fixture.CLDR != "48.0" || fixture.Unicode != "17.0" || len(fixture.Pairs) != 14532 || len(fixture.Oracle) != 15 {
		t.Fatal("oracle provenance/inventory changed")
	}
	pairs := make([][2]string, len(fixture.Pairs))
	for i, pair := range fixture.Pairs {
		for j, v := range pair {
			parsed, err := jsjson.Parse(v)
			if err != nil {
				t.Fatal(err)
			}
			pairs[i][j] = parsed.(string)
		}
	}
	for _, oracle := range fixture.Oracle {
		t.Run(oracle.Locale, func(t *testing.T) {
			c, err := New(oracle.Locale)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			info := c.Info()
			t.Logf("%+v", info)
			if info.RequestedLocale != oracle.Resolved.Locale || oracle.Resolved.Sensitivity != "variant" || oracle.Resolved.Usage != "sort" {
				t.Fatal("locale/strength mismatch")
			}
			if (info.Numeric == 17) != oracle.Resolved.Numeric || (info.AlternateHandling == 20) != oracle.Resolved.IgnorePunctuation {
				t.Fatal("numeric/punctuation mismatch", info)
			}
			caseFirst := map[int]string{16: "false", 24: "lower", 25: "upper"}[info.CaseFirst]
			if caseFirst != oracle.Resolved.CaseFirst {
				t.Fatal("case-first mismatch", info)
			}
			if len(oracle.Results) != len(pairs) {
				t.Fatal("incomplete oracle")
			}
			for i, pair := range pairs {
				got, err := c.Compare(pair[0], pair[1])
				if err != nil {
					t.Fatal(err)
				}
				if got != oracle.Results[i] {
					t.Fatalf("pair %d %q vs %q: got %d want %d", i, pair[0], pair[1], got, oracle.Results[i])
				}
			}
		})
	}
}

func TestBoundaryAndLifetime(t *testing.T) {
	for _, locale := range []string{"", "\x00en-US", "zz-QQ", "en-US!", "en_US"} {
		if c, err := New(locale); err == nil {
			c.Close()
			t.Fatalf("accepted unresolved/malformed locale %q", locale)
		}
	}
	c, err := New("en-US")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Compare("\xff", "a"); !errors.Is(err, jsjson.ErrStringEncoding) {
		t.Fatal(err)
	}
	if got, err := c.Compare("a\x00b", "a\x00c"); err != nil || got != -1 {
		t.Fatalf("NUL length lost: %d %v", got, err)
	}
	if got, err := c.Compare(jsjson.FromUTF16([]uint16{0xd800}), jsjson.FromUTF16([]uint16{0xd801})); err != nil || got != -1 {
		t.Fatalf("surrogate identity lost: %d %v", got, err)
	}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_, err := c.Compare("a", "b")
				if err != nil && !errors.Is(err, ErrClosed) {
					t.Error(err)
				}
			}
		}()
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, err := c.Compare("a", "b"); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

// These valid tags produce ICU locale IDs just below, exactly at, and above
// the former 157-byte stack buffer. Private-use data does not alter collation.
func TestLocaleBufferBoundary(t *testing.T) {
	baseline, err := New("en")
	if err != nil {
		t.Fatal(err)
	}
	defer baseline.Close()
	for _, repeats := range []int{16, 17, 18, 128} {
		tag := "en-x" + strings.Repeat("-abcdefgh", repeats)
		c, err := New(tag)
		if err != nil {
			t.Fatalf("%d private-use subtags: %v", repeats, err)
		}
		for _, pair := range [][2]string{{"a", "b"}, {"A", "a"}, {"a\x00b", "a\x00c"}} {
			want, err := baseline.Compare(pair[0], pair[1])
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.Compare(pair[0], pair[1])
			if err != nil || got != want {
				t.Fatalf("%d subtags: got %d, %v; want %d", repeats, got, err, want)
			}
		}
		c.Close()
	}
}
