// imagecodec-probe is a standalone distribution verifier. It reads no source
// tree, node_modules or Node runtime. All inputs are explicit artifact paths.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/imagecodec"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"os"
)

type row struct {
	Name, Input, Output, Error string
	Limits                     struct{ MaxImageBytes, MaxWidth, MaxHeight *int }
	FirstFrame, Cancelled      bool
	CancelAfterChecks          int
}
type countingContext struct {
	context.Context
	checks, after int
}

func (c *countingContext) Err() error {
	c.checks++
	if c.checks >= c.after {
		return context.Canceled
	}
	return nil
}
func main() {
	lib := flag.String("library", "native/linux-amd64/libvips-cpp.so.8.18.7", "native runtime artifact")
	fixture := flag.String("fixtures", "source-oracle.json", "oracle artifact")
	output := flag.String("output", "native-results.json", "actual encoded output artifact")
	flag.Parse()
	c, e := imagecodec.New(*lib)
	if e != nil {
		fatal(e)
	}
	b, e := os.ReadFile(*fixture)
	if e != nil {
		fatal(e)
	}
	var f struct{ Cases []row }
	if e = json.Unmarshal(b, &f); e != nil {
		fatal(e)
	}
	results := make([]map[string]any, 0, len(f.Cases))
	failed := 0
	for _, r := range f.Cases {
		input, e := base64.StdEncoding.DecodeString(r.Input)
		if e != nil {
			fatal(e)
		}
		ctx, cancel := context.WithCancel(context.Background())
		if r.Cancelled {
			cancel()
		}
		var selected context.Context = ctx
		if r.CancelAfterChecks > 0 {
			selected = &countingContext{Context: ctx, after: r.CancelAfterChecks}
		}
		got, e := c.Compress(selected, input, providertransport.ImageUploadLimits{MaxImageBytes: r.Limits.MaxImageBytes, MaxWidth: r.Limits.MaxWidth, MaxHeight: r.Limits.MaxHeight}, r.FirstFrame)
		cancel()
		errText := ""
		if e != nil {
			errText = e.Error()
		}
		match := got == r.Output && errText == r.Error
		if !match {
			failed++
		}
		results = append(results, map[string]any{"name": r.Name, "output": got, "error": errText, "match": match})
	}
	b, e = json.MarshalIndent(results, "", "  ")
	if e != nil {
		fatal(e)
	}
	if e = os.WriteFile(*output, append(b, '\n'), 0644); e != nil {
		fatal(e)
	}
	fmt.Printf("native image source differential: %d cases, %d failures\n", len(results), failed)
	if failed > 0 {
		os.Exit(1)
	}
}
func fatal(e error) { fmt.Fprintln(os.Stderr, e); os.Exit(1) }
