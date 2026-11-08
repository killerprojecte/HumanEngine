//go:build cgo

package jsnumber

import (
	"encoding/json"
	"math"
	"os"
	"runtime"
	"strconv"
	"testing"
)

func TestActualNodePowBits(t *testing.T) {
	checkPowBits(t, "testdata/pow-node.json")
}

// This is additional evidence, never a replacement for the frozen test.
func TestHostNodePowBits(t *testing.T) {
	path := os.Getenv("HE_POW_ORACLE")
	if path == "" {
		t.Skip("HE_POW_ORACLE not set; cross-host Node comparison not run")
	}
	checkPowBits(t, path)
}

func checkPowBits(t *testing.T, path string) {
	t.Helper()
	var fixture struct {
		Runtime json.RawMessage                       `json:"runtime"`
		Cases   []struct{ Name, X, Y, Result string } `json:"cases"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	t.Logf("oracle %s; Go target %s/%s", fixture.Runtime, runtime.GOOS, runtime.GOARCH)
	parse := func(s string) float64 {
		bits, err := strconv.ParseUint(s, 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		return math.Float64frombits(bits)
	}
	for _, c := range fixture.Cases {
		x, y := parse(c.X), parse(c.Y)
		got := Pow(x, y)
		if c.Result == "NaN" {
			if !math.IsNaN(got) {
				t.Errorf("%s: pow(%x,%x) = %016x, want NaN", c.Name, math.Float64bits(x), math.Float64bits(y), math.Float64bits(got))
			}
			continue
		}
		want := parse(c.Result)
		if math.Float64bits(got) != math.Float64bits(want) {
			t.Errorf("%s: pow(%016x,%016x) = %016x (%g), want %s (%g)", c.Name, math.Float64bits(x), math.Float64bits(y), math.Float64bits(got), got, c.Result, want)
		}
	}
}
