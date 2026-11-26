//go:build linux

package outboundmedia

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxMemorySlurpBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic-proc")
	for _, fixture := range []struct {
		data, want string
		size       int
	}{{"", "", 32}, {"123456789", "1234", 5}, {"12\x00ignored", "12", 32}, {"max\n", "max\n", 32}} {
		if err := os.WriteFile(path, []byte(fixture.data), 0600); err != nil {
			t.Fatal(err)
		}
		got, ok := slurpMemoryFile(path, fixture.size)
		if !ok || got != fixture.want {
			t.Fatalf("got %q/%v want %q", got, ok, fixture.want)
		}
	}
	if _, ok := slurpMemoryFile(path+"-absent", 32); ok {
		t.Fatal("missing probe was accepted")
	}
}
