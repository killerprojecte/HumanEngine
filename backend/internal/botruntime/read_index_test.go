package botruntime

import (
	"strconv"
	"testing"
)

func TestCachedMessagesByID(t *testing.T) {
	first, second, last := obj("id", "1"), obj("id", "2"), obj("id", "1", "text", "last")
	got := cachedMessagesByID([]*Object{first, second, last}, []string{"2", "missing", "1", "2"})
	if len(got) != 3 || got["2"] != second || got["missing"] != nil || got["1"] != first {
		t.Fatalf("unexpected results: %v", got)
	}
}

func BenchmarkCachedMessagesByID(b *testing.B) {
	const history = 4096
	rows := make([]*Object, history)
	for i := range rows {
		id := strconv.Itoa(i)
		rows[i] = obj("id", id)
	}
	ids := []string{"4095", "4087", "2048", "4040", "missing", "16", "200", "3000", "3200", "3600"}
	var sink *Object
	b.Run("copy_scan", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			snapshot := append([]*Object{}, rows...)
			out := make([]*Object, len(ids))
			for i, id := range ids {
				for _, m := range snapshot {
					if idOf(m) == id {
						out[i] = m
						break
					}
				}
			}
			sink = out[0]
		}
	})
	b.Run("one_pass", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			sink = cachedMessagesByID(rows, ids)[ids[0]]
		}
	})
	if sink != rows[history-1] {
		b.Fatal("invalid result")
	}
}
