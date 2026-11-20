//go:build humanengine_icu && cgo

package topiccollation

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

//go:embed testdata/requested-locale.json
var requestedLocaleOracle []byte

func TestRequestedLocaleFromPinnedNode(t *testing.T) {
	var oracle struct {
		Node                     string
		Input, Sorted, TwoLetter []string
		Cases                    []struct {
			Tag           string
			Lower, Sorted []string
		}
	}
	if err := json.Unmarshal(requestedLocaleOracle, &oracle); err != nil || oracle.Node != SourceNode {
		t.Fatalf("requested locale oracle: %s %v", oracle.Node, err)
	}
	for a := 'a'; a <= 'z'; a++ {
		for b := 'a'; b <= 'z'; b++ {
			tag := string([]rune{a, b})
			r, err := NewRequested(tag)
			if slices.Contains(oracle.TwoLetter, tag) {
				if err != nil {
					t.Errorf("%s: %v", tag, err)
				} else {
					r.Close()
				}
			} else if !errors.Is(err, ErrLocale) {
				if r != nil {
					r.Close()
				}
				t.Errorf("%s: want unsupported; got %v", tag, err)
			}
		}
	}
	for _, c := range oracle.Cases {
		t.Run(c.Tag, func(t *testing.T) {
			r, err := NewRequested(c.Tag)
			if c.Lower == nil {
				if r != nil {
					r.Close()
				}
				if !errors.Is(err, ErrLocale) {
					t.Errorf("want invalid locale; got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			for i, in := range oracle.Input {
				got, err := r.Lower(in)
				if err != nil || got != c.Lower[i] {
					t.Errorf("lower(%q): got %q (%v), want %q", in, got, err, c.Lower[i])
				}
			}
			type key struct {
				raw    string
				weight []byte
			}
			keys := make([]key, len(oracle.Sorted))
			for i, item := range oracle.Sorted {
				weight, err := r.SortKey(item)
				if err != nil {
					t.Fatal(err)
				}
				keys[i] = key{item, weight}
			}
			slices.SortStableFunc(keys, func(a, b key) int { return bytes.Compare(a.weight, b.weight) })
			got := make([]string, len(keys))
			for i, key := range keys {
				got[i] = key.raw
			}
			if !slices.Equal(got, c.Sorted) {
				t.Errorf("sort: got %q; want %q", got, c.Sorted)
			}
		})
	}
}
