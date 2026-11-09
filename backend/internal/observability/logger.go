// Package observability separates event production from potentially slow log IO.
package observability

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

var ErrClosed = errors.New("log_writer_closed")

type Event struct {
	ID         string         `json:"id"`
	At         time.Time      `json:"at"`
	Level      string         `json:"level"`
	Module     string         `json:"module"`
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	RequestID  string         `json:"requestId,omitempty"`
	TraceID    string         `json:"traceId,omitempty"`
	SceneID    string         `json:"sceneId,omitempty"`
	TurnID     string         `json:"turnId,omitempty"`
	Stage      string         `json:"stage,omitempty"`
	DurationMS float64        `json:"durationMs,omitempty"`
	Fields     map[string]any `json:"fields,omitempty"`
}
type Status struct {
	InFlight      int
	InFlightBytes int64
	Queued        int
	QueuedBytes   int64
	Written       uint64
	Failed        uint64
	Closed        bool
	LastError     string
}
type queued struct {
	json  []byte
	human []byte
}
type Logger struct {
	closedRejections, unserializable, failedSinkRecords uint64
	noticeAt                                            time.Time
	retained                                            retainedLogs
	inFlight                                            int
	inFlightBytes                                       int64
	mu                                                  sync.Mutex
	wake                                                *sync.Cond
	queue                                               []queued
	queuedBytes                                         int64
	closing                                             bool
	done                                                chan struct{}
	jsonSink, humanSink                                 io.Writer
	written, failed                                     uint64
	lastError                                           string
}

func New(jsonSink, humanSink io.Writer) *Logger {
	l := &Logger{jsonSink: jsonSink, humanSink: humanSink, done: make(chan struct{})}
	l.wake = sync.NewCond(&l.mu)
	go l.run()
	return l
}

// Emit snapshots the caller's fields before returning. IO never runs on the caller.
// There is no silent truncation or admission cap; backlog and IO failures are visible.
func (l *Logger) Emit(e Event) error {
	if e.ID == "" {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return err
		}
		id[6] = (id[6] & 0x0f) | 0x40
		id[8] = (id[8] & 0x3f) | 0x80
		e.ID = fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	// Snapshot caller-owned fields exactly once. Besides avoiding duplicate
	// encoding work, this keeps JSON and human output identical even when a
	// custom marshaler has state or side effects.
	var fields json.RawMessage
	var err error
	if len(e.Fields) != 0 {
		fields, err = json.Marshal(e.Fields)
		if err != nil {
			l.encodingFailed()
			return fmt.Errorf("encode diagnostic %s: %w", e.Code, err)
		}
	}
	e.Fields = nil
	data, err := json.Marshal(struct {
		Event
		Fields json.RawMessage `json:"fields,omitempty"`
	}{Event: e, Fields: fields})
	if err != nil {
		l.encodingFailed()
		return fmt.Errorf("encode diagnostic %s: %w", e.Code, err)
	}
	retained, retainedLevel, err := retainedProjection(e, fields)
	if err != nil {
		l.encodingFailed()
		return fmt.Errorf("encode retained diagnostic %s: %w", e.Code, err)
	}
	entry := queued{json: append(data, '\n')}
	if l.humanSink != nil {
		entry.human = []byte(renderSnapshot(e, fields) + "\n")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.retained.add(retained, retainedLevel)
	if l.closing {
		l.closedRejections++
		l.noticeLocked(time.Now())
		return ErrClosed
	}
	l.queue = append(l.queue, entry)
	l.queuedBytes += int64(len(entry.json) + len(entry.human))
	l.wake.Signal()
	l.noticeLocked(time.Now())
	return nil
}
func (l *Logger) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.noticeLocked(time.Now())
	return Status{InFlight: l.inFlight, InFlightBytes: l.inFlightBytes, Queued: len(l.queue), QueuedBytes: l.queuedBytes, Written: l.written, Failed: l.failed, Closed: l.closing, LastError: l.lastError}
}
func (l *Logger) Close(ctx context.Context) error {
	l.mu.Lock()
	l.closing = true
	l.wake.Broadcast()
	l.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.done:
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed > 0 {
		return fmt.Errorf("log_delivery_incomplete: %d writes failed: %s", l.failed, l.lastError)
	}
	return nil
}
func (l *Logger) run() {
	defer close(l.done)
	for {
		l.mu.Lock()
		for len(l.queue) == 0 && !l.closing {
			l.wake.Wait()
		}
		if len(l.queue) == 0 && l.closing {
			l.mu.Unlock()
			return
		}
		// Detach the batch in O(1); producers immediately continue on a fresh slice.
		batch := l.queue
		l.inFlight = len(batch)
		l.inFlightBytes = l.queuedBytes
		l.queue = nil
		l.queuedBytes = 0
		l.mu.Unlock()
		for _, e := range batch {
			var err error
			var sinkFailures uint64
			if l.jsonSink != nil {
				err = writeAll(l.jsonSink, e.json)
				if err != nil {
					sinkFailures++
				}
			}
			if l.humanSink != nil {
				humanErr := writeAll(l.humanSink, e.human)
				if humanErr != nil {
					sinkFailures++
				}
				err = errors.Join(err, humanErr)
			}
			l.mu.Lock()
			l.failedSinkRecords += sinkFailures
			l.inFlight--
			l.inFlightBytes -= int64(len(e.json) + len(e.human))
			if err != nil {
				l.failed++
				l.lastError = err.Error()
			} else {
				l.written++
			}
			l.mu.Unlock()
		}
	}
}
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n <= 0 || n > len(b) {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func Render(e Event) string {
	var fields []byte
	if len(e.Fields) != 0 {
		fields, _ = json.Marshal(e.Fields)
	}
	return renderSnapshot(e, fields)
}
func renderSnapshot(e Event, fields []byte) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s %-5s %s · %s | %s", e.At.UTC().Format(time.RFC3339Nano), strings.ToUpper(e.Level), e.Module, e.Code, e.Message)
	for _, pair := range [][2]string{{"trace", e.TraceID}, {"scene", e.SceneID}, {"turn", e.TurnID}, {"request", e.RequestID}, {"stage", e.Stage}} {
		if pair[1] != "" {
			fmt.Fprintf(&out, " %s=%s", pair[0], pair[1])
		}
	}
	if e.DurationMS != 0 {
		fmt.Fprintf(&out, " duration=%.3fms", e.DurationMS)
	}
	if len(fields) > 0 {
		fmt.Fprintf(&out, " %s", fields)
	}
	return out.String()
}
