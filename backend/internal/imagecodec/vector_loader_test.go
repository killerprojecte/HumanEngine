//go:build linux && amd64 && cgo && bundled_sqlite_vec

package imagecodec

import (
	"database/sql"
	"os"
	"os/exec"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/vectorasset"
	"github.com/mattn/go-sqlite3"
)

func TestNativeImageAndVectorLoaderPathOwnership(t *testing.T) {
	const child = "HE_TEST_NATIVE_IMAGE_VECTOR_CHILD"
	if os.Getenv(child) == "" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestNativeImageAndVectorLoaderPathOwnership$")
		cmd.Env = append(os.Environ(), child+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fresh-process native loaders: %v\n%s", err, output)
		}
		return
	}

	// Reserve the next two descriptor slots before the first libvips init.
	// New reopens the first slot for the input, then the second for its memfd.
	// Filling the first slot afterward makes Vec reuse the libvips pathname
	// if the mapped image memfd was closed but its dlopen handle stayed live.
	first, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	lower := first.Fd()
	first.Close()
	second.Close()
	compressor, cases := loadCases(t)
	block, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer block.Close()
	if block.Fd() != lower {
		t.Fatalf("expected the lower descriptor slot %d, got %d", lower, block.Fd())
	}
	vector, err := vectorasset.Extract()
	if err != nil {
		t.Fatal(err)
	}
	defer vector.Close()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := vector.Use(func(path string) error {
		return conn.Raw(func(raw any) error {
			return raw.(*sqlite3.SQLiteConn).LoadExtension(path, "sqlite3_vec_init")
		})
	}); err != nil {
		t.Fatal(err)
	}
	var version string
	if err = conn.QueryRowContext(t.Context(), "SELECT vec_version()").Scan(&version); err != nil ||
		version != "v"+vectorasset.Version {
		t.Fatalf("native sqlite-vec: %q %v", version, err)
	}
	for _, c := range cases {
		if c.Error == "" && c.Output != "" {
			got, err := runCase(compressor, c)
			if err := caseMismatch(c, got, err); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("missing successful real image fixture")
}
