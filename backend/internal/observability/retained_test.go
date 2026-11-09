package observability

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRetainedActualSourceLimits(t *testing.T) {
	data, err := os.ReadFile("../botruntime/testdata/status-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Retention []struct {
			Name    string
			Entries []struct {
				Level string
				Bytes int
			}
			Result struct {
				IDs                []string
				Bytes              int
				Oversized, Evicted uint64
				RecentErrors       int
			}
		}
	}
	if err = json.Unmarshal(data, &source); err != nil {
		t.Fatal(err)
	}
	for _, c := range source.Retention {
		t.Run(c.Name, func(t *testing.T) {
			l := &Logger{}
			for _, e := range c.Entries {
				l.retained.add(make([]byte, e.Bytes), e.Level)
			}
			got := l.Retention()
			if got.Records != len(c.Result.IDs) || got.Bytes != c.Result.Bytes || got.Oversized != c.Result.Oversized || got.Evicted != c.Result.Evicted || got.RecentErrors != c.Result.RecentErrors {
				t.Fatalf("%+v source %+v", got, c.Result)
			}
		})
	}
}
func TestRetainedEmitBeforeClosedAdmission(t *testing.T) {
	l := New(nil, nil)
	if err := l.Emit(Event{Level: "error", Code: "before"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := l.Emit(Event{Level: "error", Code: "after"}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if got := l.Retention(); got.RecentErrors != 2 || got.Records != 3 {
		t.Fatal(got)
	}
}

func TestRetainedNoticeActualSourceLifecycle(t *testing.T) {
	data, err := os.ReadFile("../botruntime/testdata/status-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Notices []struct {
			Name                              string
			Records, RecentErrors, Warnings   int
			Closed, Unserializable, Oversized uint64
		}
	}
	if err = json.Unmarshal(data, &source); err != nil {
		t.Fatal(err)
	}
	l := New(nil, nil)
	defer l.Close(context.Background())
	assertStep := func(index int) {
		t.Helper()
		expected := source.Notices[index]
		got := l.Retention()
		warnings := 0
		l.mu.Lock()
		for _, row := range l.retained.rows {
			var value struct{ Code string }
			if err := json.Unmarshal(row.json, &value); err != nil {
				t.Fatal(err)
			}
			if value.Code == "log_delivery_degraded" {
				warnings++
			}
		}
		closed, bad := l.closedRejections, l.unserializable
		l.mu.Unlock()
		if got.Records != expected.Records || got.RecentErrors != expected.RecentErrors || warnings != expected.Warnings || closed != expected.Closed || bad != expected.Unserializable || got.Oversized != expected.Oversized {
			t.Fatalf("%s got %+v warnings=%d closed=%d bad=%d source %+v", expected.Name, got, warnings, closed, bad, expected)
		}
	}
	if err = l.Emit(Event{Level: "error", Module: "synthetic", Code: "before"}); err != nil {
		t.Fatal(err)
	}
	assertStep(0)
	if err = l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = l.Emit(Event{Level: "error", Module: "synthetic", Code: "after"}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	assertStep(1)
	if err = l.Emit(Event{Level: "error", Code: "bad", Fields: map[string]any{"unsupported": make(chan int)}}); err == nil {
		t.Fatal("expected encoding failure")
	}
	assertStep(2)
	l.mu.Lock()
	l.noticeAt = l.noticeAt.Add(-time.Minute)
	l.mu.Unlock()
	if err = l.Emit(Event{Level: "error", Code: "oversized", Fields: map[string]any{"payload": strings.Repeat("x", retainedByteLimit)}}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	assertStep(3)
}
