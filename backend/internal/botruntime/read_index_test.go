package botruntime

import (
	"strconv"
	"testing"
)

func TestCachedMessages(t *testing.T) {
	first, second, last := obj("id", "1"), obj("id", "2"), obj("id", "1", "text", "last")
	root := &messageBlock{previous: &messageBlock{rows: []*Object{first}}, rows: []*Object{second, last}}
	got := cachedMessages(root, []string{"2", "missing", "1", "2"})
	if len(got) != 4 || got[0] != second || got[1] != nil || got[2] != first || got[3] != second {
		t.Fatalf("unexpected results: %v", got)
	}
	if got := cachedMessages(root, []string{"1"}); len(got) != 1 || got[0] != first {
		t.Fatalf("singleton lookup did not retain first row: %v", got)
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
	var root *messageBlock
	for _, row := range rows {
		root = appendRecord(root, &messageRecord{body: row})
	}
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
			sink = cachedMessages(root, ids)[0]
		}
	})
	if sink != rows[history-1] {
		b.Fatal("invalid result")
	}
}
