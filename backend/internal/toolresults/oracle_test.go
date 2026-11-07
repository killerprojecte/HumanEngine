package toolresults

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type fixtureSource struct {
	Kind                       string
	Units                      []uint16
	Repeat, Length, ChunkBytes int
}
type fixtureStep struct {
	Op      string
	Source  fixtureSource
	Options struct {
		ContentType  json.RawMessage
		Encoding     Encoding
		PreviewBytes *float64
	}
	Input               ReadInput
	Save, Handle, Scope string
	Owner               *struct {
		Scope, SceneID, PluginID string
		Dependencies             []string
	}
	Guard, Cancel, Adopt, AdoptInvalidate bool
	AdoptError                            string
	Value                                 bool
	Error                                 string
	FalseAt                               int
	Now                                   float64
	SceneID, MessageID, PluginID          string
}
type observed struct {
	Result                          string   `json:"result,omitempty"`
	NestedBytes                     int      `json:"nestedBytes,omitempty"`
	DecodedSHA256                   string   `json:"decodedSHA256,omitempty"`
	ContentBytes                    int      `json:"contentBytes,omitempty"`
	Error                           string   `json:"error,omitempty"`
	Adopted                         []string `json:"adopted"`
	Entries, Files, Pending, Writes int
}
type oracle struct {
	Version                    int
	Node, Source, SourceSHA256 string
	Traces                     []struct {
		Name     string
		TTL, Now float64
		Steps    []struct {
			Input    fixtureStep
			Expected observed
		}
	}
	Stalled []struct {
		Mode, Error                                 string
		Returns, Destroys, Pending, Writes, Entries int
	}
}

func fixtures(t *testing.T) oracle {
	t.Helper()
	data, err := os.ReadFile("testdata/source-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture oracle
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != 1 || fixture.Node != "v24.22.1" || fixture.SourceSHA256 != "6a74dee6988aca28f22adb1f392dd8d7cd75e1dea23922fc7a8b280e1375a08b" {
		t.Fatal("unexpected source oracle")
	}
	source, err := os.ReadFile("../../../src/tool-results.ts")
	if err != nil {
		t.Fatal(err)
	}
	if digest(source) != fixture.SourceSHA256 {
		t.Fatal("actual TypeScript oracle changed")
	}
	return fixture
}
func digest(bytes []byte) string { sum := sha256.Sum256(bytes); return hex.EncodeToString(sum[:]) }
func decode(t *testing.T, result Result) []byte {
	t.Helper()
	if result.Encoding == UTF8 {
		return []byte(result.Content)
	}
	bytes, err := base64.StdEncoding.DecodeString(result.Content)
	if err != nil {
		t.Fatal(err)
	}
	return bytes
}
func observation(t *testing.T, result Result) observed {
	t.Helper()
	first, err := jsjson.Stringify(result.Content)
	if err != nil {
		t.Fatal(err)
	}
	second, err := jsjson.Stringify(first)
	if err != nil {
		t.Fatal(err)
	}
	out := observed{NestedBytes: len(second), ContentBytes: len(result.Content), DecodedSHA256: digest(decode(t, result))}
	result.ResultID = "<result>"
	result.Content = digest([]byte(result.Content))
	value, err := result.JSONValue()
	if err != nil {
		t.Fatal(err)
	}
	out.Result, err = jsjson.Stringify(value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func fixtureInput(spec fixtureSource) Source {
	if spec.Kind == "text" {
		return Text(strings.Repeat(jsjson.FromUTF16(spec.Units), spec.Repeat))
	}
	length := spec.Length
	if spec.Kind == "failed" || spec.Kind == "toolError" || spec.Kind == "invalidChunk" {
		length = 4
	}
	data := make([]byte, length)
	for i := range data {
		data[i] = byte(i % 256)
	}
	if spec.Kind == "bytes" {
		return Bytes(data)
	}
	offset := 0
	return NextFunc(func(context.Context) ([]byte, error) {
		if spec.Kind == "invalidChunk" {
			return nil, errors.New("invalid_tool_result_chunk")
		}
		if offset < len(data) {
			n := spec.ChunkBytes
			if n == 0 {
				n = 4
			}
			end := min(offset+n, len(data))
			chunk := data[offset:end]
			offset = end
			return chunk, nil
		}
		if spec.Kind == "failed" {
			return nil, errors.New("fixture_source_failed")
		}
		if spec.Kind == "toolError" {
			return nil, errors.New("tool_result_source_error")
		}
		return nil, io.EOF
	})
}
func testOwner() Owner {
	return Owner{Scope: "real:scene-a", SceneID: "scene-a", PluginID: "fixture", Dependencies: []string{"message-a"}}
}
func num(value float64) *float64 { return &value }

func TestActualSourceDifferential(t *testing.T) {
	for _, trace := range fixtures(t).Traces {
		t.Run(trace.Name, func(t *testing.T) {
			var clock atomic.Uint64
			clock.Store(math.Float64bits(trace.Now))
			config := Config{TTLMS: num(trace.TTL), Now: func() float64 { return math.Float64frombits(clock.Load()) }, TempDir: t.TempDir()}
			cache := New(config)
			defer func() {
				if err := cache.Dispose(); err != nil {
					t.Error(err)
				}
			}()
			handles := make(map[string]string)
			guard := fixtureStep{Value: true}
			calls := 0
			for i, step := range trace.Steps {
				input := step.Input
				out := observed{Adopted: []string{}}
				ctx := context.Background()
				if input.Cancel {
					canceled, cancel := context.WithCancelCause(ctx)
					cancel(errors.New("fixture_cancelled"))
					ctx = canceled
				}
				handle := input.Handle
				if handle == "" {
					handle = "latest"
				}
				id := handles[handle]
				if id == "" {
					id = "missing"
				}
				var result Result
				var err error
				hasResult := false
				switch input.Op {
				case "store":
					owner := testOwner()
					if input.Owner != nil {
						if input.Owner.Scope != "" {
							owner.Scope = input.Owner.Scope
						}
						if input.Owner.SceneID != "" {
							owner.SceneID = input.Owner.SceneID
						}
						if input.Owner.PluginID != "" {
							owner.PluginID = input.Owner.PluginID
						}
						if input.Owner.Dependencies != nil {
							owner.Dependencies = input.Owner.Dependencies
						}
					}
					if input.Guard {
						owner.Validations = []Validation{func() (bool, error) {
							calls++
							if guard.Error != "" {
								return false, errors.New(guard.Error)
							}
							if guard.FalseAt != 0 {
								return calls < guard.FalseAt, nil
							}
							return guard.Value, nil
						}}
					}
					options := Options{Encoding: input.Options.Encoding, PreviewBytes: input.Options.PreviewBytes}
					if len(input.Options.ContentType) > 0 {
						value, parseErr := jsjson.Parse(string(input.Options.ContentType))
						if parseErr != nil {
							t.Fatal(parseErr)
						}
						contentType := value.(string)
						options.ContentType = &contentType
					}
					result, err = cache.Store(ctx, fixtureInput(input.Source), owner, options)
					if err == nil {
						save := input.Save
						if save == "" {
							save = "latest"
						}
						handles[save] = result.ResultID
						hasResult = true
					}
				case "read":
					request := input.Input
					request.ResultID = id
					scope := input.Scope
					if scope == "" {
						scope = "real:scene-a"
					}
					var adopt func(Owner) error
					if input.Adopt || input.AdoptError != "" || input.AdoptInvalidate {
						adopt = func(owner Owner) error {
							out.Adopted = append(out.Adopted, owner.Dependencies...)
							if input.AdoptError != "" {
								return errors.New(input.AdoptError)
							}
							if input.AdoptInvalidate {
								cache.InvalidatePlugin(owner.PluginID)
							}
							return nil
						}
					}
					result, err = cache.Read(ctx, request, scope, adopt)
					hasResult = err == nil
				case "guard":
					guard = input
					calls = 0
				case "time":
					clock.Store(math.Float64bits(input.Now))
				case "expire":
					cache.Expire()
				case "invalidate":
					cache.Invalidate(input.SceneID, input.MessageID)
				case "invalidatePlugin":
					cache.InvalidatePlugin(input.PluginID)
				case "truncate":
					err = os.Truncate(cache.entries[id].path, 0)
				case "unlink":
					err = os.Remove(cache.entries[id].path)
				case "dispose":
					err = cache.Dispose()
				case "restart":
					err = cache.Dispose()
					cache = New(config)
				default:
					t.Fatalf("unknown step %s", input.Op)
				}
				if hasResult {
					adopted := out.Adopted
					out = observation(t, result)
					out.Adopted = adopted
				}
				if err != nil {
					out.Error = err.Error()
				}
				cache.mu.Lock()
				out.Entries = len(cache.entries)
				out.Writes = len(cache.writes)
				out.Pending = len(cache.writes)
				cache.mu.Unlock()
				if cache.directory != "" {
					files, readErr := os.ReadDir(cache.directory)
					if readErr != nil && !os.IsNotExist(readErr) {
						t.Fatal(readErr)
					}
					out.Files = len(files)
				}
				if !reflect.DeepEqual(out, step.Expected) {
					a, _ := json.Marshal(out)
					b, _ := json.Marshal(step.Expected)
					t.Fatalf("step %d %s\nactual %s\nsource %s", i, input.Op, a, b)
				}
			}
		})
	}
}
