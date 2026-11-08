package imagecodec

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type sourceCase struct {
	Name, Input, Output, Error string
	Limits                     struct{ MaxImageBytes, MaxWidth, MaxHeight *int }
	FirstFrame, Cancelled      bool
	CancelAfterChecks          int
}
type checkedContext struct {
	context.Context
	calls, after int
}

func (c *checkedContext) Err() error {
	c.calls++
	if c.calls >= c.after {
		return context.Canceled
	}
	return nil
}
func loadCases(t *testing.T) (*Compressor, []sourceCase) {
	t.Helper()
	path := os.Getenv("HUMANENGINE_VIPS_LIBRARY")
	if path == "" {
		t.Fatal("HUMANENGINE_VIPS_LIBRARY is required; refusing a false green without pixels")
	}
	c, e := New(path)
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile("testdata/source.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct{ Cases []sourceCase }
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	return c, f.Cases
}
func runCase(c *Compressor, v sourceCase) (string, error) {
	b, e := base64.StdEncoding.DecodeString(v.Input)
	if e != nil {
		return "", e
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if v.Cancelled {
		cancel()
	}
	var selected context.Context = ctx
	if v.CancelAfterChecks > 0 {
		selected = &checkedContext{Context: ctx, after: v.CancelAfterChecks}
	}
	return c.Compress(selected, b, providertransport.ImageUploadLimits{MaxImageBytes: v.Limits.MaxImageBytes, MaxWidth: v.Limits.MaxWidth, MaxHeight: v.Limits.MaxHeight}, v.FirstFrame)
}
func caseMismatch(v sourceCase, got string, e error) error {
	if v.Error != "" {
		if e == nil || e.Error() != v.Error {
			return fmt.Errorf("error: got %v want %s", e, v.Error)
		}
		return nil
	}
	if e != nil {
		return e
	}
	if got != v.Output {
		return fmt.Errorf("encoded pixel bytes differ: got length %d want %d", len(got), len(v.Output))
	}
	return nil
}
func TestActualSourcePixels(t *testing.T) {
	c, cases := loadCases(t)
	for _, v := range cases {
		t.Run(v.Name, func(t *testing.T) {
			got, e := runCase(c, v)
			if dir := os.Getenv("HUMANENGINE_IMAGE_OUTPUTS"); dir != "" {
				if e2 := os.MkdirAll(dir, 0755); e2 != nil {
					t.Fatal(e2)
				}
				result := struct {
					Output string `json:"output,omitempty"`
					Error  string `json:"error,omitempty"`
				}{Output: got}
				if e != nil {
					result.Error = e.Error()
				}
				b, _ := json.Marshal(result)
				if e2 := os.WriteFile(filepath.Join(dir, v.Name+".json"), b, 0644); e2 != nil {
					t.Fatal(e2)
				}
			}
			if err := caseMismatch(v, got, e); err != nil {
				os.MkdirAll("failures", 0755)
				os.WriteFile("failures/"+v.Name+".actual.txt", []byte(got), 0644)
				os.WriteFile("failures/"+v.Name+".expected.txt", []byte(v.Output), 0644)
				t.Fatal(err)
			}
		})
	}
}
func TestConcurrentPixelsAndCachedInputRelease(t *testing.T) {
	c, cases := loadCases(t)
	wanted := map[string]bool{"alpha-resize": true, "jpeg-search": true, "corrupt": true, "jpeg-warning-truncated": true, "animated-first-frame": true, "rgb16-p3-resize": true, "thin-100x1-webp": true, "cancel-during-jpeg-search": true}
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, v := range cases {
				if wanted[v.Name] {
					got, e := runCase(c, v)
					if mismatch := caseMismatch(v, got, e); mismatch != nil {
						errs <- fmt.Errorf("%s: %w", v.Name, mismatch)
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	// Only after all native requests have joined: evict cached graphs and verify
	// every owned encoded input was released by its native finalizer.
	trimCacheForTest()
	if n := liveInputsForTest(); n != 0 {
		t.Errorf("native encoded-input owners after cache drain: %d", n)
	}
}
func TestRejectedLibraryDoesNotLeakDescriptors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrong.so")
	if e := os.WriteFile(path, []byte("not the approved binary"), 0600); e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadDir("/proc/self/fd")
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 8; i++ {
		_, e := New(path)
		if e == nil || e.Error() != "native_image_library_hash_mismatch" {
			t.Fatalf("unexpected library result: %v", e)
		}
	}
	after, e := os.ReadDir("/proc/self/fd")
	if e != nil {
		t.Fatal(e)
	}
	if len(after) != len(before) {
		t.Fatalf("descriptor leak: before %d after %d", len(before), len(after))
	}
}

func TestLargePixelBoundary(t *testing.T) {
	if os.Getenv("HUMANENGINE_RUN_LARGE_IMAGE_BOUNDARY") != "1" {
		t.Skip("separate resource window required for real 40M-pixel source fixture")
	}
	c, _ := loadCases(t)
	b, e := os.ReadFile("testdata/large-boundary.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct{ Cases []sourceCase }
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	for _, v := range f.Cases {
		t.Run(v.Name, func(t *testing.T) {
			got, e := runCase(c, v)
			if e = caseMismatch(v, got, e); e != nil {
				t.Fatal(e)
			}
		})
	}
}
