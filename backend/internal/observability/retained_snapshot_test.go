package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

func TestRetainedSnapshotOwnedAndOrdered(t *testing.T) {
	l := New(nil, nil)
	defer l.Close(context.Background())
	if string(l.RetainedJSON()) != "[]" {
		t.Fatal("empty is not array")
	}
	for i := 0; i < 503; i++ {
		if err := l.Emit(Event{ID: fmt.Sprint(i), Level: "info", Code: "synthetic"}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := l.RetainedJSON()
	var rows []struct{ ID string }
	if err := json.Unmarshal(snapshot, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 500 || rows[0].ID != "502" || rows[499].ID != "3" {
		t.Fatal(rows)
	}
	snapshot[1] = 'x'
	if !json.Valid(l.RetainedJSON()) {
		t.Fatal("caller mutated retained bytes")
	}
	before := l.Retention()
	l.RetainedJSON()
	after := l.Retention()
	if before != after {
		t.Fatal("snapshot mutated owner")
	}
}
func TestRetainedSnapshotConcurrentProducer(t *testing.T) {
	l := New(nil, nil)
	defer l.Close(context.Background())
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 600; i++ {
				if err := l.Emit(Event{Level: "info", Code: "synthetic"}); err != nil {
					t.Error(err)
					return
				}
				var rows []json.RawMessage
				if err := json.Unmarshal(l.RetainedJSON(), &rows); err != nil || len(rows) > 500 {
					t.Error("inconsistent snapshot", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
func TestRetainedSnapshotDoesNotGenerateNotice(t *testing.T) {
	l := New(nil, nil)
	defer l.Close(context.Background())
	l.mu.Lock()
	l.failedSinkRecords = 1
	l.mu.Unlock()
	if string(l.RetainedJSON()) != "[]" {
		t.Fatal("read created notice absent from source logs getter")
	}
	l.Status()
	var rows []struct{ Code string }
	if err := json.Unmarshal(l.RetainedJSON(), &rows); err != nil || len(rows) != 1 || rows[0].Code != "log_delivery_degraded" {
		t.Fatal(rows, err)
	}
}
