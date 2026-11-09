package snowlumatransport

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

// Compare JSON structure without routing lone UTF-16 surrogates through encoding/json.
func normalizationValue(v any) any {
	switch v := v.(type) {
	case *jsjson.Object:
		result := map[string]any{}
		for _, member := range v.Members() {
			result[member.Key] = normalizationValue(member.Value)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = normalizationValue(item)
		}
		return result
	default:
		return v
	}
}

func TestNormalizationActualSourceAndGuest(t *testing.T) {
	raw, err := os.ReadFile("../../core/testdata/normalization-source.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jsjson.Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := parsed.([]any)
	if !ok || len(rows) == 0 {
		t.Fatal("missing actual source rows")
	}
	path := filepath.Join(t.TempDir(), "snowluma.wasm")
	build := exec.Command("go", "build", "-buildvcs=false", "-buildmode=c-shared", "-o", path, "../../wasm")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual guest: %v %s", err, out)
	}
	artifact, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual guest SHA256 %x", sha256.Sum256(artifact))
	host, err := plugins.NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	if err := host.Enable(context.Background(), "snowluma", artifact); err != nil {
		t.Fatal(err)
	}
	call, err := host.BindCall("snowluma")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range rows {
		row := value.(*jsjson.Object)
		name, _ := row.Get("name")
		input, _ := row.Get("input")
		expected, _ := row.Get("expected")
		t.Run(name.(string), func(t *testing.T) {
			wire, err := jsjson.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			out, err := call(context.Background(), wire)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := jsjson.Parse(string(out))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(normalizationValue(actual), normalizationValue(expected)) {
				want, _ := jsjson.Stringify(expected)
				t.Fatalf("actual %s\nsource %s", out, want)
			}
		})
	}
}
