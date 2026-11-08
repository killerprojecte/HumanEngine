//go:build !bundled_sqlite_vec || !linux || !amd64

package vectorasset

import (
	"os"
	"testing"
)

func TestUnsupportedBundleDoesNotExtract(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	if a, e := Extract(); e == nil {
		a.Close()
		t.Fatal("unsupported bundle accepted")
	}
	entries, e := os.ReadDir(root)
	if e != nil || len(entries) != 0 {
		t.Fatal("unsupported bundle wrote files", entries, e)
	}
}
