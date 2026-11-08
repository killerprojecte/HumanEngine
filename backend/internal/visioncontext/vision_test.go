package visioncontext

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSourceOracle(t *testing.T) {
	data, e := os.ReadFile("testdata/source.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		SourceSHA256, PNG, AlternatePNG, FixedTime string
		Cases                                      []struct {
			Name     string
			Messages []struct {
				ID, SceneID, Status, SentAt string
				Parts                       []struct{ Type, MediaType, AssetID string }
			}
			Result, Second                          json.RawMessage
			Reads, Calls, Recognitions, Validations int
			Dependencies, CacheKeys                 []string
			Prompts                                 []struct {
				Role     string
				Messages []struct {
					Role, Content string
					Images        []string
				}
				Tools []any
			}
		}
	}
	if e = json.Unmarshal(data, &fixture); e != nil {
		t.Fatal(e)
	}
	if fixture.SourceSHA256 != "b3f016006bd2633123eeb330e13dc4048f0af610872dea27b762ea11fa5b2002" {
		t.Fatal("source fingerprint mismatch")
	}
	png, _ := base64.StdEncoding.DecodeString(fixture.PNG)
	alternatePNG, _ := base64.StdEncoding.DecodeString(fixture.AlternatePNG)
	now, _ := time.Parse(time.RFC3339Nano, fixture.FixedTime)
	for _, f := range fixture.Cases {
		t.Run(f.Name, func(t *testing.T) {
			models, _ := jsjson.Parse(`{"routes":[{"role":"vision","orderedModelIds":["synthetic"]}]}`)
			cfg := Config{SceneID: "s", Mode: "real", Timezone: "UTC", Models: models, VisionRoute: true, Now: func() time.Time { return now }}
			if f.Name == "simulation" {
				cfg.Mode = "simulation"
			}
			if f.Name == "no_route" {
				cfg.VisionRoute = false
				cfg.Models, _ = jsjson.Parse(`{"routes":[]}`)
			}
			if f.Name == "priority" {
				cfg.PriorityIDs = []string{"m0", "m2"}
			}
			if f.Name == "model_order" {
				cfg.Models, _ = jsjson.Parse(`{"routes":[{"role":"vision","orderedModelIds":["synthetic"]}],"providers":[]}`)
			}
			ms := []*Message{}
			for _, m := range f.Messages {
				v := &Message{ID: m.ID, SceneID: m.SceneID, Status: m.Status, SentAt: m.SentAt}
				for _, p := range m.Parts {
					v.Parts = append(v.Parts, Part{Type: p.Type, MediaType: p.MediaType, AssetID: p.AssetID})
				}
				ms = append(ms, v)
			}
			reads, calls, recognitions, validations := 0, 0, 0, 0
			deps := []string{}
			seen := map[string]bool{}
			p := Ports{Recalled: func(string, string) (bool, error) { return false, nil }, SourceAvailable: func(c Candidate) (bool, error) { return !(f.Name == "unavailable" && c.Message.ID == "m0"), nil }, ReadImage: func(_ context.Context, c Candidate) ([]byte, error) {
				reads++
				if f.Name == "failure" {
					return nil, errors.New("public_http_403")
				}
				if f.Name == "unavailable" {
					return nil, errors.New("image_payload_invalid")
				}
				if f.Name == "distinct_bytes" && c.Part.AssetID == "a1" {
					return alternatePNG, nil
				}
				return png, nil
			}, ImageDataURL: func(context.Context, []byte) (string, error) {
				if calls >= len(f.Prompts) {
					t.Fatal("unexpected decode")
				}
				return f.Prompts[calls].Messages[1].Images[0], nil
			}, Complete: func(_ context.Context, msgs []ChatMessage) (string, error) {
				if calls >= len(f.Prompts) {
					t.Fatal("unexpected completion")
				}
				expected := f.Prompts[calls]
				if expected.Role != "vision" || len(expected.Tools) != 0 || len(msgs) != len(expected.Messages) {
					t.Fatal("completion shape differs")
				}
				for i, m := range msgs {
					v := expected.Messages[i]
					if m.Role != v.Role || m.Content != v.Content || !reflect.DeepEqual(m.Images, v.Images) {
						t.Fatalf("full prompt mismatch: %#v / %#v", m, v)
					}
				}
				calls++
				if f.Name == "empty" {
					return "   ", nil
				}
				if f.Name == "utf16" {
					return strings.Repeat("a", 3999) + "😀", nil
				}
				return " synthetic vision ", nil
			}, AddDependency: func(id string) {
				if !seen[id] {
					deps = append(deps, id)
					seen[id] = true
				}
			}, AddSourceValidation: func(Candidate) { validations++ }}
			var lib *Library
			if f.Name == "library" || f.Name == "library_empty" || f.Name == "seeded_empty" {
				lib = &Library{AddLookupValidation: func(Candidate, string) { validations++ }, Lookup: func(Candidate) (string, bool, error) { return "synthetic observation", f.Name == "library", nil }, Recognize: func(context.Context, []byte, Candidate) (string, bool, error) { recognitions++; return "", true, nil }}
			}
			turn := &Turn{}
			cache := &Cache{}
			if f.Name == "seeded_empty" {
				key, _ := hash("visual-expression-v2", "s", fmt.Sprintf("%x", sha256.Sum256(png)), cfg.Models)
				cache.Put(key, "s", "seed", "cached description")
			}
			first := ms
			if f.Name == "content_cache" {
				first = ms[:1]
			}
			out, e := Describe(context.Background(), turn, cfg, first, cache, p, lib)
			if e != nil {
				t.Fatal(e)
			}
			check := func(got Result, want json.RawMessage) {
				b, e := json.Marshal(got)
				if e != nil {
					t.Fatal(e)
				}
				a, e := jsjson.ParseBytes(b)
				if e != nil {
					t.Fatal(e)
				}
				z, e := jsjson.ParseBytes(want)
				if e != nil {
					t.Fatal(e)
				}
				as, _ := jsjson.Stringify(a)
				zs, _ := jsjson.Stringify(z)
				if as != zs {
					t.Fatalf("full serialized result mismatch: got %s want %s", as, zs)
				}
				projected, e := got.Project()
				if e != nil {
					t.Fatal(e)
				}
				ps, _ := jsjson.Stringify(projected)
				if ps != zs {
					t.Fatal("native projection differs")
				}
			}
			check(out, f.Result)
			if len(f.Second) > 0 {
				second := ms
				if f.Name == "content_cache" {
					turn = &Turn{}
					second = ms[1:]
					p.AddDependency = func(string) {}
				}
				if f.Name == "model_order" {
					cfg.Models, _ = jsjson.Parse(`{"providers":[],"routes":[{"role":"vision","orderedModelIds":["synthetic"]}]}`)
				}
				out, e = Describe(context.Background(), turn, cfg, second, cache, p, lib)
				if e != nil {
					t.Fatal(e)
				}
				check(out, f.Second)
			}
			if reads != f.Reads || calls != f.Calls || recognitions != f.Recognitions || validations != f.Validations || !reflect.DeepEqual(deps, f.Dependencies) {
				t.Fatalf("counts reads %d calls %d recognitions %d validations %d deps %v; source %+v", reads, calls, recognitions, validations, deps, f)
			}
			for _, key := range f.CacheKeys {
				if _, ok := cache.Get(key); !ok {
					t.Fatalf("source cache key missing: %s", key)
				}
			}
		})
	}
}
func TestCacheSourcePolicy(t *testing.T) {
	now := time.Unix(1, 0)
	c := &Cache{Now: func() time.Time { return now }}
	for i := 0; i < 64; i++ {
		c.Put(fmt.Sprint(i), "s", "m", "d")
	}
	c.Get("0")
	c.Put("64", "s", "m", "d")
	if _, ok := c.Get("0"); ok {
		t.Fatal("Get must not refresh FIFO")
	}
	c.Invalidate("other", "m")
	if _, ok := c.Get("64"); !ok {
		t.Fatal("scene isolation")
	}
	now = now.Add(5 * time.Minute)
	if _, ok := c.Get("64"); ok {
		t.Fatal("expiry inclusive")
	}
	c.Put("x", "s", "m", "d")
	c.Invalidate("s", "m")
	if _, ok := c.Get("x"); ok {
		t.Fatal("invalidation")
	}
}
func TestUTF16ProductLimit(t *testing.T) {
	got, e := trimSlice("\ufeff" + strings.Repeat("a", 3999) + "😀 ")
	if e != nil {
		t.Fatal(e)
	}
	units, _ := jsjson.UTF16(got)
	if len(units) != 4000 || units[3999] != 0xd83d {
		t.Fatalf("not JS slice: %v", units[len(units)-2:])
	}
}

func TestAuthorityAndLibraryRevision(t *testing.T) {
	available := true
	stored := "revision one"
	reads, sourceValidations, lookupValidations := 0, 0, 0
	model, _ := jsjson.Parse(`{"routes":[]}`)
	cfg := Config{SceneID: "s", Mode: "real", Models: model}
	m := &Message{ID: "m", SceneID: "s", Status: "available", Parts: []Part{{Type: "media", MediaType: "image", AssetID: "a"}}}
	p := Ports{Recalled: func(string, string) (bool, error) { return false, nil }, SourceAvailable: func(Candidate) (bool, error) { return available, nil }, ReadImage: func(context.Context, Candidate) ([]byte, error) {
		reads++
		available = false
		return []byte("synthetic"), nil
	}, ImageDataURL: func(context.Context, []byte) (string, error) { t.Fatal("decode after invalidation"); return "", nil }, Complete: func(context.Context, []ChatMessage) (string, error) {
		t.Fatal("model after invalidation")
		return "", nil
	}, AddDependency: func(string) {}, AddSourceValidation: func(Candidate) { sourceValidations++ }}
	lib := &Library{Lookup: func(Candidate) (string, bool, error) { return stored, stored != "", nil }, AddLookupValidation: func(_ Candidate, s string) {
		lookupValidations++
		if s != stored {
			t.Fatal("wrong stored revision")
		}
	}, Recognize: func(context.Context, []byte, Candidate) (string, bool, error) {
		t.Fatal("recognize after invalidation")
		return "", false, nil
	}}
	turn := &Turn{}
	cache := &Cache{}
	out, e := Describe(context.Background(), turn, cfg, []*Message{m}, cache, p, lib)
	if e != nil || out.Images[0].Description != "revision one" {
		t.Fatal(out, e)
	}
	stored = "revision two"
	out, e = Describe(context.Background(), turn, cfg, []*Message{m}, cache, p, lib)
	if e != nil || out.Images[0].Description != "revision two" {
		t.Fatal(out, e)
	}
	stored = ""
	_, e = Describe(context.Background(), turn, cfg, []*Message{m}, cache, p, lib)
	if !errors.Is(e, ErrCancelled) {
		t.Fatalf("revoked observation must be reread and authority loss aborts: %v", e)
	}
	if reads != 1 || sourceValidations != 3 || lookupValidations != 2 {
		t.Fatal(reads, sourceValidations, lookupValidations)
	}
}

func TestErrorProjection(t *testing.T) {
	for _, v := range []struct{ in, out string }{{"public_http_404", "public_http_404"}, {"public_http_300", "image_description_unavailable"}, {"secret endpoint token", "image_description_unavailable"}, {"request_timeout", "request_timeout"}} {
		if got := FailureReason(errors.New(v.in)); got != v.out {
			t.Fatal(got, v.out)
		}
	}
}

func TestConcurrentCache(t *testing.T) {
	var wg sync.WaitGroup
	c := &Cache{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				key := fmt.Sprint(i, j)
				c.Put(key, "s", "m", "synthetic")
				c.Get(key)
				c.Invalidate("s", "other")
			}
		}(i)
	}
	wg.Wait()
	c.Clear()
}

func TestPromptSourceOracle(t *testing.T) {
	b, e := os.ReadFile("testdata/source.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Cases []struct{ SystemPrompt, UserInstruction string }
	}
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	checked := false
	for _, c := range f.Cases {
		if c.SystemPrompt != "" {
			checked = true
			if c.SystemPrompt != SystemPrompt || c.UserInstruction != UserInstruction {
				t.Fatal("model prompt differs from actual source")
			}
		}
	}
	if !checked {
		t.Fatal("oracle did not exercise provider path")
	}
}

func TestAuthorityRevocationAfterEachAsyncStage(t *testing.T) {
	for _, stage := range []string{"recognize", "decode", "complete"} {
		t.Run(stage, func(t *testing.T) {
			available := true
			decoded, completed := 0, 0
			models, _ := jsjson.Parse(`{}`)
			cfg := Config{SceneID: "s", Mode: "real", Models: models, VisionRoute: true}
			m := &Message{ID: "m", SceneID: "s", Status: "available", Parts: []Part{{Type: "media", MediaType: "image", AssetID: "a"}}}
			p := Ports{Recalled: func(string, string) (bool, error) { return false, nil }, SourceAvailable: func(Candidate) (bool, error) { return available, nil }, ReadImage: func(context.Context, Candidate) ([]byte, error) { return []byte("synthetic"), nil }, ImageDataURL: func(context.Context, []byte) (string, error) {
				decoded++
				if stage == "decode" {
					available = false
				}
				return "data:image/png;base64,c3ludGhldGlj", nil
			}, Complete: func(context.Context, []ChatMessage) (string, error) {
				completed++
				if stage == "complete" {
					available = false
				}
				return "must not escape", nil
			}, AddDependency: func(string) {}, AddSourceValidation: func(Candidate) {}}
			lib := &Library{Recognize: func(context.Context, []byte, Candidate) (string, bool, error) {
				if stage == "recognize" {
					available = false
				}
				return "", false, nil
			}}
			cache := &Cache{}
			turn := &Turn{}
			out, e := Describe(context.Background(), turn, cfg, []*Message{m}, cache, p, lib)
			if !errors.Is(e, ErrCancelled) || len(out.Images) != 0 || len(cache.entries) != 0 || len(turn.results) != 0 {
				t.Fatal("revoked output escaped", out, e)
			}
			if stage == "recognize" && (decoded != 0 || completed != 0) || stage == "decode" && (decoded != 1 || completed != 0) || stage == "complete" && (decoded != 1 || completed != 1) {
				t.Fatal("continued past authority loss", decoded, completed)
			}
		})
	}
}

func TestConcurrentDescribeSharesTurnBudget(t *testing.T) {
	models, _ := jsjson.Parse(`{}`)
	cfg := Config{SceneID: "s", Mode: "real", Models: models, VisionRoute: true}
	ms := []*Message{}
	for i := 0; i < 6; i++ {
		ms = append(ms, &Message{ID: fmt.Sprint(i), SceneID: "s", Status: "available", Parts: []Part{{Type: "media", MediaType: "image", AssetID: fmt.Sprint(i)}}})
	}
	reads, calls := 0, 0
	p := Ports{Recalled: func(string, string) (bool, error) { return false, nil }, SourceAvailable: func(Candidate) (bool, error) { return true, nil }, ReadImage: func(context.Context, Candidate) ([]byte, error) { reads++; return []byte("synthetic"), nil }, ImageDataURL: func(context.Context, []byte) (string, error) { return "data:image/png;base64,c3ludGhldGlj", nil }, Complete: func(context.Context, []ChatMessage) (string, error) { calls++; return "synthetic", nil }, AddDependency: func(string) {}, AddSourceValidation: func(Candidate) {}}
	turn := &Turn{}
	cache := &Cache{}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out, e := Describe(context.Background(), turn, cfg, ms, cache, p, nil)
			if e != nil || len(out.Images) != 4 || len(out.Omitted) != 2 {
				t.Errorf("shared turn result %v %v", out, e)
			}
		}()
	}
	close(start)
	wg.Wait()
	if reads != 4 || calls != 1 || len(turn.budget) != 4 {
		t.Fatal("shared turn duplicated budget/work", reads, calls, len(turn.budget))
	}
}
