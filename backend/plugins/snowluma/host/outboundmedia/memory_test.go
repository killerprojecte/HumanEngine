package outboundmedia

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

type memoryRead struct {
	Path string
	Size int
}

func TestOfficialLibuvMemoryFixtures(t *testing.T) {
	var fixtures []struct {
		Name                    string
		Files                   map[string]string
		Want, SysFree, SysTotal uint64
		SysOK                   bool
	}
	data, err := os.ReadFile("testdata/libuv-memory/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	var source []struct {
		Name      string
		Reads     []memoryRead
		Available uint64
	}
	if path := os.Getenv("HE_OUTBOUND_MEMORY_ORACLE"); path != "" {
		data, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, &source); err != nil {
			t.Fatal(err)
		}
		if len(source) != len(fixtures) {
			t.Fatal("upstream oracle case count mismatch")
		}
	} else {
		t.Log("Using fixture expectations; set HE_OUTBOUND_MEMORY_ORACLE for actual upstream C read-order comparison")
	}
	for i, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			reads := []memoryRead{}
			probe := linuxMemoryProbe{
				slurp: func(path string, size int) (string, bool) {
					reads = append(reads, memoryRead{path, size})
					text, ok := f.Files[path]
					if !ok {
						return "", false
					}
					if len(text) >= size {
						text = text[:size-1]
					}
					if at := strings.IndexByte(text, 0); at >= 0 {
						text = text[:at]
					}
					return text, true
				},
				sysinfo: func() (uint64, uint64, bool) {
					reads = append(reads, memoryRead{"sysinfo", 0})
					return f.SysFree, f.SysTotal, f.SysOK
				},
				pageSize: 4096, longMax: 1<<63 - 1,
			}
			got := probe.available()
			if got != f.Want {
				t.Fatalf("got %d want %d", got, f.Want)
			}
			if len(source) > 0 {
				actual := source[i]
				if actual.Name != f.Name || actual.Available != got {
					t.Fatalf("upstream result mismatch: %+v, native %d", actual, got)
				}
				if !reflect.DeepEqual(reads, actual.Reads) {
					t.Fatalf("IO order differs from actual upstream: native %+v, source %+v", reads, actual.Reads)
				}
			}
		})
	}
}
