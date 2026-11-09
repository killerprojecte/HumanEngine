package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRetainedProjectionActualSourceRows(t *testing.T) {
	raw, err := os.ReadFile("retained-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Cases []struct {
			Name        string
			Event       json.RawMessage
			Text, Level string
			Bytes       int
		}
	}
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	for _, c := range source.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var event Event
			if err := json.Unmarshal(c.Event, &event); err != nil {
				t.Fatal(err)
			}
			var rawEvent struct{ Fields map[string]json.RawMessage }
			if err := json.Unmarshal(c.Event, &rawEvent); err != nil {
				t.Fatal(err)
			}
			event.Fields = make(map[string]any, len(rawEvent.Fields))
			for key, raw := range rawEvent.Fields {
				event.Fields[key] = raw
			}

			fields, err := json.Marshal(event.Fields)
			if err != nil {
				t.Fatal(err)
			}
			data, level, err := retainedProjection(event, fields)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err = json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal([]byte(c.Text), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) || len(data) != c.Bytes || level != c.Level {
				t.Fatalf("projection %s (%d/%s) source %s (%d/%s)", data, len(data), level, c.Text, c.Bytes, c.Level)
			}
			var sink bytes.Buffer
			logger := New(&sink, nil)
			if err = logger.Emit(event); err != nil {
				t.Fatal(err)
			}
			if err = logger.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := logger.Retention(); got.Bytes != c.Bytes || got.Records != 1 {
				t.Fatalf("actual Emit retention %+v", got)
			}
			// Legacy sinks keep caller message and nested Fields. The source projection
			// never rewrites unrelated diagnostics output.

			expectedSink, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(sink.Bytes(), append(expectedSink, '\n')) {
				t.Fatalf("legacy sink bytes changed: %s want %s", sink.Bytes(), expectedSink)
			}

		})
	}
}
func TestRetainedProjectionActualSourceInteractions(t *testing.T) {
	raw, err := os.ReadFile("retained-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Interactions []struct {
			Name                                  string
			Records, Bytes, RecentErrors, Notices int
			Evicted, Oversized                    uint64
		}
	}
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	logger := New(nil, nil)
	defer logger.Close(context.Background())
	at, err := time.Parse(time.RFC3339Nano, "2026-11-09T00:00:00.123Z")
	if err != nil {
		t.Fatal(err)
	}
	event := Event{ID: "11111111-1111-4111-8111-111111111111", At: at, Level: "error", Module: "synthetic", Code: "ordinary", Fields: map[string]any{"extra": strings.Repeat("🙂", 240)}}
	check := func(index int) {
		t.Helper()
		want := source.Interactions[index]
		got := logger.Retention()
		notices := 0
		logger.mu.Lock()
		for _, row := range logger.retained.rows {
			var value struct{ Code string }
			if err := json.Unmarshal(row.json, &value); err != nil {
				t.Fatal(err)
			}
			if value.Code == "log_delivery_degraded" {
				notices++
			}
		}
		logger.mu.Unlock()
		if got.Records != want.Records || got.Bytes != want.Bytes || got.RecentErrors != want.RecentErrors || got.Evicted != want.Evicted || got.Oversized != want.Oversized || notices != want.Notices {
			t.Fatalf("%s got %+v notices=%d source %+v", want.Name, got, notices, want)
		}
	}
	for i := 0; i < 500; i++ {
		if err = logger.Emit(event); err != nil {
			t.Fatal(err)
		}
	}
	check(0)
	if err = logger.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	event.Level = "info"
	event.Code = "closed"
	event.Fields = nil
	logger.Emit(event)
	check(1)
	event.Code = "oversized"
	event.Fields = map[string]any{"extra": strings.Repeat("🙂", 1024*1024)}
	logger.Emit(event)
	check(2)
	logger.mu.Lock()
	logger.noticeAt = logger.noticeAt.Add(-time.Minute)
	logger.mu.Unlock()
	event.Code = "closed"
	event.Fields = nil
	logger.Emit(event)
	check(3)
}

func TestRetainedProjectionActualSourceByteBoundaries(t *testing.T) {
	raw, err := os.ReadFile("retained-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Boundaries []struct {
			Name      string
			Event     Event
			FillBytes int
			Follow    bool
			Result    struct {
				Records, Bytes, RecentErrors, Notices, Enqueued int
				Evicted, Oversized                              uint64
			}
		}
	}
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	for _, c := range source.Boundaries {
		t.Run(c.Name, func(t *testing.T) {
			logger := New(nil, nil)
			c.Event.Fields = map[string]any{"extra": strings.Repeat("x", c.FillBytes)}
			if err := logger.Emit(c.Event); err != nil {
				t.Fatal(err)
			}
			if c.Follow {
				c.Event.Level = "info"
				c.Event.Code = "small"
				c.Event.Fields = nil
				if err := logger.Emit(c.Event); err != nil {
					t.Fatal(err)
				}
			}
			if err := logger.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := logger.Retention()
			notices := 0
			logger.mu.Lock()
			for _, row := range logger.retained.rows {
				var value struct{ Code string }
				if err := json.Unmarshal(row.json, &value); err != nil {
					t.Fatal(err)
				}
				if value.Code == "log_delivery_degraded" {
					notices++
				}
			}
			logger.mu.Unlock()
			want := c.Result
			if got.Records != want.Records || got.Bytes != want.Bytes || got.RecentErrors != want.RecentErrors || got.Evicted != want.Evicted || got.Oversized != want.Oversized || notices != want.Notices {
				t.Fatalf("%+v notices=%d source %+v", got, notices, want)
			}
			if got := logger.Status().Written; got != uint64(want.Enqueued) {
				t.Fatalf("retention altered sink admission: written=%d source enqueued=%d", got, want.Enqueued)
			}
		})
	}
}

type retainedFailureSink struct{ calls int }

func (s *retainedFailureSink) Write([]byte) (int, error) { s.calls++; return 0, io.ErrClosedPipe }
func TestRetainedFailureNoticeDoesNotReenterSinks(t *testing.T) {
	raw, err := os.ReadFile("retained-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		SinkFailure struct {
			Before struct{ Records, Bytes int }
			After  struct {
				Records, Bytes, RecentErrors int
				NoticeCounts                 map[string]any
			}
			SinkEnqueues, AfterEnqueues int
		}
	}
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	sink := new(retainedFailureSink)
	logger := New(sink, sink)
	at, err := time.Parse(time.RFC3339Nano, "2026-11-09T00:00:00.123Z")
	if err != nil {
		t.Fatal(err)
	}
	if err = logger.Emit(Event{ID: "11111111-1111-4111-8111-111111111111", At: at, Level: "error", Module: "synthetic", Code: "sink-failed"}); err != nil {
		t.Fatal(err)
	}
	if err = logger.Close(context.Background()); err == nil {
		t.Fatal("lost real sink failure")
	}
	before := logger.Retention()
	want := source.SinkFailure
	if before.Records != want.Before.Records || before.Bytes != want.Before.Bytes || sink.calls != want.SinkEnqueues {
		t.Fatal(before, sink.calls, want)
	}
	logger.Status()
	after := logger.Retention()
	if after.Records != want.After.Records || after.Bytes != want.After.Bytes || after.RecentErrors != want.After.RecentErrors || sink.calls != want.AfterEnqueues {
		t.Fatal(after, sink.calls, want)
	}
	var notice struct{ Counts map[string]any }
	if err = json.Unmarshal(logger.retained.rows[len(logger.retained.rows)-1].json, &notice); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(notice.Counts, want.After.NoticeCounts) {
		t.Fatal(notice.Counts, want.After.NoticeCounts)
	}
}
