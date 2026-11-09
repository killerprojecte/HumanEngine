package logdelivery

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func limit(n int64) *int64 { return &n }

type step struct {
	n   int
	err error
}
type memorySink struct {
	operations                   [][]any
	offset                       int
	mu                           sync.Mutex
	bytes                        []byte
	steps                        []step
	writes                       int
	syncErr, openErr, errorClose error
	closed                       int
	closeGate                    <-chan struct{}
	writeGate                    <-chan struct{}
	entered                      chan struct{}
}

func (s *memorySink) prepare() error { s.mu.Lock(); s.offset = 0; s.mu.Unlock(); return s.openErr }
func (s *memorySink) write(p []byte) (int, error) {
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	if s.writeGate != nil {
		<-s.writeGate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	s.operations = append(s.operations, []any{"write", "<stdout>", s.offset, len(p)})
	st := step{n: len(p)}
	if len(s.steps) > 0 {
		st = s.steps[0]
		s.steps = s.steps[1:]
	}
	n := min(st.n, len(p))
	s.bytes = append(s.bytes, p[:n]...)
	s.offset += n
	return n, st.err
}
func (s *memorySink) sync() error { return s.syncErr }
func (s *memorySink) close() error {
	if s.closeGate != nil {
		<-s.closeGate
	}
	s.mu.Lock()
	s.closed++
	s.mu.Unlock()
	return s.errorClose
}
func (s *memorySink) text() string { s.mu.Lock(); defer s.mu.Unlock(); return string(s.bytes) }
func gated(t *testing.T, opts Options, s sink) (*Channel, chan struct{}) {
	t.Helper()
	gate := make(chan struct{})
	c, e := newChannel(opts, s, gate, nil)
	if e != nil {
		t.Fatal(e)
	}
	c.flushStarted = make(chan struct{}, 16)
	t.Cleanup(func() {
		c.mu.Lock()
		c.abandonLocked("worker_exited")
		c.mu.Unlock()
		select {
		case <-c.Done():
		case <-time.After(3 * time.Second):
			t.Error("physical owner did not exit")
		}
	})
	return c, gate
}

type flushAnswer struct {
	result FlushResult
	err    error
}

func beginFlush(t *testing.T, c *Channel, timeout time.Duration, closing bool) <-chan flushAnswer {
	t.Helper()
	select {
	case <-c.flushStarted:
	default:
	}
	done := make(chan flushAnswer, 1)
	go func() { r, e := c.Flush(context.Background(), timeout, closing); done <- flushAnswer{r, e} }()
	select {
	case <-c.flushStarted:
	case a := <-done:
		done <- a
	case <-time.After(3 * time.Second):
		t.Fatal("barrier was not admitted")
	}
	return done
}
func answer(t *testing.T, done <-chan flushAnswer) FlushResult {
	t.Helper()
	select {
	case a := <-done:
		if a.err != nil {
			t.Fatal(a.err)
		}
		return a.result
	case <-time.After(3 * time.Second):
		t.Fatal("barrier did not settle")
	}
	return FlushResult{}
}
func flush(t *testing.T, c *Channel, closeSink bool) FlushResult {
	t.Helper()
	r, e := c.Flush(context.Background(), DefaultTimeout, closeSink)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func sourceCases(t *testing.T) map[string]map[string]json.RawMessage {
	t.Helper()
	raw, e := os.ReadFile("../log-channel-memory-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var doc struct {
		SourceHash string                       `json:"sourceHash"`
		Cases      []map[string]json.RawMessage `json:"cases"`
	}
	if e = json.Unmarshal(raw, &doc); e != nil {
		t.Fatal(e)
	}
	if doc.SourceHash != "e39bdbc29f063267991b38ec8a36e903dbd12388e2d1cb803915ede65dac8060" {
		t.Fatal("source changed")
	}
	result := make(map[string]map[string]json.RawMessage)
	for _, c := range doc.Cases {
		var n string
		json.Unmarshal(c["name"], &n)
		result[n] = c
	}
	if len(result) != 25 {
		t.Fatal(len(result))
	}
	return result
}
func exact(t *testing.T, cases map[string]map[string]json.RawMessage, name, key string, got any) {
	t.Helper()
	want, ok := cases[name][key]
	if !ok {
		t.Fatalf("missing source %s/%s", name, key)
	}
	b, e := json.Marshal(got)
	if e != nil {
		t.Fatal(e)
	}
	var a, v any
	json.Unmarshal(want, &a)
	json.Unmarshal(b, &v)
	if !reflect.DeepEqual(a, v) {
		t.Fatalf("%s/%s\n got %s\nwant %s", name, key, b, want)
	}
}
func TestSourceChannelOracle(t *testing.T) {
	cases := sourceCases(t)
	t.Run("default-limits-and-option-validation", func(t *testing.T) {
		name := t.Name()[len("TestSourceChannelOracle/"):]
		var initial []Status
		var closed []FlushResult
		for _, opts := range []Options{{}, {Directory: "/logs"}} {
			c, g := gated(t, opts, &memorySink{})
			initial = append(initial, c.Status())
			d := beginFlush(t, c, DefaultTimeout, true)
			close(g)
			closed = append(closed, answer(t, d))
		}
		exact(t, cases, name, "initial", initial)
		exact(t, cases, name, "closed", closed)
		for _, o := range []Options{{MaxBytes: limit(0)}, {MaxRecords: limit(0)}, {MaxBytes: limit(1023)}, {MaxBytes: limit(67108865)}, {MaxRecords: limit(7)}, {MaxRecords: limit(65537)}, {MaxBytes: limit(-1)}} {
			if _, err := normalize(o); !errors.Is(err, ErrInvalidLimits) {
				t.Fatal(o, err)
			}
		}
	})
	t.Run("record-reserve-sticky-rejection-whole-fifo", func(t *testing.T) {
		n := "record-reserve-sticky-rejection-whole-fifo"
		s := &memorySink{}
		c, g := gated(t, Options{Directory: "/logs", MaxBytes: limit(1024), MaxRecords: limit(8)}, s)
		var admissions []bool
		for i := 0; i < 8; i++ {
			admissions = append(admissions, c.Enqueue(string(rune('0'+i))+"\n", false))
		}
		admissions = append(admissions, c.Enqueue("priority\n", true), c.Enqueue(strings.Repeat("x", 1025), true))
		exact(t, cases, n, "admission", admissions)
		exact(t, cases, n, "pending", c.Status())
		d := beginFlush(t, c, DefaultTimeout, true)
		close(g)
		exact(t, cases, n, "result", answer(t, d))
		exact(t, cases, n, "bytes", s.text())
	})
	t.Run("exact-byte-reserve-unicode-empty-and-allocation-failure", func(t *testing.T) {
		n := "exact-byte-reserve-unicode-empty-and-allocation-failure"
		s := &memorySink{}
		c, g := gated(t, Options{MaxBytes: limit(1024), MaxRecords: limit(8)}, s)
		a := []bool{c.Enqueue(strings.Repeat("🚀", 224), false), c.Enqueue("a", false), c.Enqueue(strings.Repeat("x", 128), true), c.Enqueue("", true)}
		exact(t, cases, n, "full", c.Status())
		d := beginFlush(t, c, DefaultTimeout, false)
		close(g)
		answer(t, d)
		c.mu.Lock()
		c.encodeFailure = true
		c.mu.Unlock()
		a = append(a, c.Enqueue("漢字🚀\xed\xa0\x80\n", true))
		c.mu.Lock()
		c.encodeFailure = false
		c.mu.Unlock()
		a = append(a, c.Enqueue("漢字🚀\xed\xa0\x80\n", true))
		exact(t, cases, n, "admission", a)
		exact(t, cases, n, "result", flush(t, c, true))
		exact(t, cases, n, "bytesHex", hex.EncodeToString([]byte(s.text())))
	})
	t.Run("stdout-partial-eagain-retries-only-unwritten-suffix", func(t *testing.T) {
		n := "stdout-partial-eagain-retries-only-unwritten-suffix"
		s := &memorySink{steps: []step{{n: 2}, {err: syscall.EAGAIN}, {err: syscall.EWOULDBLOCK}, {n: 3}, {err: syscall.EAGAIN}}}
		c, g := gated(t, Options{}, s)
		var during []Status
		var backoffs []int64
		c.retryWait = func(d time.Duration, _ <-chan struct{}) bool {
			during = append(during, c.Status())
			backoffs = append(backoffs, d.Milliseconds())
			return true
		}
		c.Enqueue("prefix:🚀\n", false)
		d := beginFlush(t, c, DefaultTimeout, true)
		close(g)
		exact(t, cases, n, "result", answer(t, d))
		exact(t, cases, n, "during", during)
		exact(t, cases, n, "backoffs", backoffs)
		exact(t, cases, n, "bytes", s.text())
	})
	t.Run("ambiguous-partial-error-is-terminal-no-replay", func(t *testing.T) {
		n := "ambiguous-partial-error-is-terminal-no-replay"
		s := &memorySink{steps: []step{{n: 3}, {err: syscall.EIO}}}
		c, g := gated(t, Options{}, s)
		c.Enqueue("abcdef\n", false)
		c.Enqueue("second\n", false)
		d := beginFlush(t, c, DefaultTimeout, false)
		close(g)
		answer(t, d)
		exact(t, cases, n, "beforeReject", c.Status())
		exact(t, cases, n, "admitted", c.Enqueue("after\n", false))
		exact(t, cases, n, "result", flush(t, c, true))
		exact(t, cases, n, "operations", s.operations)
		if s.text() != "abc" || s.writes != 2 {
			t.Fatal(s.text(), s.writes)
		}
	})
	for _, name := range []string{"fsync-failure-does-not-invent-failed-record", "zero-write-is-terminal", "file-open-failure-is-sink-failure-not-construction-failure", "sync-close-drains-existing-batch-and-fsync"} {
		t.Run(name, func(t *testing.T) {
			s := &memorySink{}
			opts := Options{Directory: "/logs"}
			switch name {
			case "fsync-failure-does-not-invent-failed-record":
				s.syncErr = errors.New("private/path")
			case "zero-write-is-terminal":
				opts.Directory = ""
				s.steps = []step{{n: 0}}
			case "file-open-failure-is-sink-failure-not-construction-failure":
				s.openErr = errors.New("private/path")
			}
			c, g := gated(t, opts, s)
			c.Enqueue("one\n", false)
			if name == "file-open-failure-is-sink-failure-not-construction-failure" || name == "sync-close-drains-existing-batch-and-fsync" {
				c.Enqueue("two\n", false)
			}
			d := beginFlush(t, c, DefaultTimeout, true)
			close(g)
			exact(t, cases, name, "result", answer(t, d))
		})
	}
	for _, name := range []string{"worker-error-is-unconfirmed-and-sanitized", "worker-exit-is-unconfirmed-and-sanitized"} {
		t.Run(name, func(t *testing.T) {
			c, _ := gated(t, Options{}, &memorySink{})
			c.Enqueue("one\n", false)
			reason := "worker_failed"
			if strings.Contains(name, "-exit-") {
				reason = "worker_exited"
			}
			c.mu.Lock()
			c.abandonLocked(reason)
			c.mu.Unlock()
			exact(t, cases, name, "result", flush(t, c, false))
		})
	}
	t.Run("stop-during-backpressure-never-replays-written-prefix", func(t *testing.T) {
		n := "stop-during-backpressure-never-replays-written-prefix"
		s := &memorySink{steps: []step{{n: 2}, {err: syscall.EAGAIN}}}
		c, g := gated(t, Options{}, s)
		var timed FlushResult
		c.retryWait = func(_ time.Duration, _ <-chan struct{}) bool {
			timed, _ = c.Flush(context.Background(), 0, true)
			return false
		}
		c.Enqueue("abcdef\n", false)
		close(g)
		<-c.Done()
		exact(t, cases, n, "timeoutResult", timed)
		exact(t, cases, n, "after", c.Status())
		exact(t, cases, n, "bytes", s.text())
	})
	for _, kind := range []string{"records", "flush"} {
		t.Run(kind+"-handoff-failure-is-unconfirmed", func(t *testing.T) {
			n := kind + "-handoff-failure-is-unconfirmed"
			closeGate := make(chan struct{})
			defer close(closeGate)
			s := &memorySink{closeGate: closeGate}
			c, g := gated(t, Options{}, s)
			c.handoffFailure = kind
			c.Enqueue("one\n", false)
			c.Enqueue("two\n", false)
			if kind == "records" {
				d := beginFlush(t, c, DefaultTimeout, true)
				close(g)
				exact(t, cases, n, "result", answer(t, d))
			} else {
				exact(t, cases, n, "result", flush(t, c, false))
			}
			exact(t, cases, n, "closedBeforeExit", c.Closed())
		})
	}
	t.Run("concurrent-flush-targets-own-later-admissions", func(t *testing.T) {
		n := "concurrent-flush-targets-own-later-admissions"
		s := &memorySink{}
		c, g := gated(t, Options{Directory: "/logs"}, s)
		reached := make(chan struct{}, 2)
		resume := make(chan struct{})
		c.afterControl = func() { reached <- struct{}{}; <-resume }
		c.Enqueue("one\n", false)
		first := beginFlush(t, c, DefaultTimeout, false)
		c.Enqueue("two\n", false)
		second := beginFlush(t, c, DefaultTimeout, false)
		close(g)
		<-reached
		r1 := answer(t, first)
		resume <- struct{}{}
		<-reached
		r2 := answer(t, second)
		exact(t, cases, n, "results", []FlushResult{r1, r2})
		close(resume)
		exact(t, cases, n, "close", flush(t, c, true))
	})
	t.Run("shorter-concurrent-deadline-abandons-unconfirmed", func(t *testing.T) {
		n := "shorter-concurrent-deadline-abandons-unconfirmed"
		c, _ := gated(t, Options{}, &memorySink{})
		c.Enqueue("held\n", false)
		first := beginFlush(t, c, time.Second, false)
		second := beginFlush(t, c, time.Millisecond, true)
		exact(t, cases, n, "results", []FlushResult{answer(t, first), answer(t, second)})
	})
	t.Run("file-drains-independently-of-held-stdout", func(t *testing.T) {
		n := "file-drains-independently-of-held-stdout"
		out, _ := gated(t, Options{}, &memorySink{})
		file, g := gated(t, Options{Directory: "/logs"}, &memorySink{})
		out.Enqueue("held\n", false)
		file.Enqueue("durable\n", false)
		d := beginFlush(t, file, DefaultTimeout, true)
		close(g)
		exact(t, cases, n, "fileResult", answer(t, d))
		exact(t, cases, n, "held", out.Status())
		r, _ := out.Flush(context.Background(), 0, true)
		exact(t, cases, n, "outResult", r)
	})
	t.Run("clean-close-then-enqueue-is-degraded", func(t *testing.T) {
		n := "clean-close-then-enqueue-is-degraded"
		c, g := gated(t, Options{}, &memorySink{})
		c.Enqueue("one\n", false)
		d := beginFlush(t, c, DefaultTimeout, true)
		close(g)
		exact(t, cases, n, "closed", answer(t, d))
		if c.Enqueue("no\n", false) {
			t.Fatal("accepted after close")
		}
		exact(t, cases, n, "again", flush(t, c, false))
	})
}
