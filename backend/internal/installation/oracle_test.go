package installation

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// Enable for the actual TypeScript+SQLite differential, not a reimplementation.
func TestActualSourceInstallationOracle(t *testing.T) {
	root := os.Getenv("HUMANENGINE_SOURCE_ROOT")
	if root == "" {
		t.Skip("set HUMANENGINE_SOURCE_ROOT for actual-source TS differential")
	}
	root, e := filepath.Abs(root)
	if e != nil {
		t.Fatal(e)
	}
	node := os.Getenv("HUMANENGINE_TEST_NODE")
	if node == "" {
		node = "node"
	}
	cmd := exec.Command(node, "--import", filepath.Join(root, "node_modules/tsx/dist/loader.mjs"), "testdata/oracle.mts")
	cmd.Env = append(os.Environ(), "HUMANENGINE_SOURCE_ROOT="+root)
	got, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("actual source: %v\n%s", e, got)
	}
	want, e := os.ReadFile("testdata/oracle.json")
	if e != nil {
		t.Fatal(e)
	}
	var a, b any
	if e = json.Unmarshal(got, &a); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(want, &b); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("actual source changed: regenerate and review fixture, do not silently accept")
	}
}
