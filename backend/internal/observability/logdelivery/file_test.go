package logdelivery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestSourceRotationOnRealFilesystem(t *testing.T) {
	cases := sourceCases(t)
	name := "rotation-strict-threshold-and-interrupted-tail"
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "runtime.jsonl"), []byte("interrupted:"), 0600); e != nil {
		t.Fatal(e)
	}
	var operations [][]any
	output := &fileSink{directory: dir, rotateBytes: 4, trace: func(op []any) {
		for i, v := range op {
			if p, ok := v.(string); ok && strings.HasPrefix(p, dir) {
				op[i] = "/logs" + strings.TrimPrefix(p, dir)
			}
		}
		operations = append(operations, op)
	}}
	c, g := gated(t, Options{Directory: dir, RotateBytes: limit(4)}, output)
	c.Enqueue("123\n", false)
	c.Enqueue("x\n", false)
	c.Enqueue("z\n", false)
	done := beginFlush(t, c, DefaultTimeout, true)
	close(g)
	exact(t, cases, name, "result", answer(t, done))
	<-c.Done()
	files := make(map[string]string)
	for _, suffix := range []string{"", ".1", ".2"} {
		b, e := os.ReadFile(filepath.Join(dir, "runtime.jsonl"+suffix))
		if e != nil {
			t.Fatal(e)
		}
		files["/logs/runtime.jsonl"+suffix] = string(b)
	}
	exact(t, cases, name, "files", files)
	exact(t, cases, name, "operations", operations)
}
func TestRealFileLazyStatusPermissionsAndReopen(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "new", "logs")
	r := NewRegistry(io.Discard)
	if len(r.owners) != 0 {
		t.Fatal("registry construction acquired owner")
	}
	l := lease(t, r, directory)
	for i := 0; i < 10; i++ {
		s := l.Status()
		if s.State != "ready" || s.LastFailure != nil {
			t.Fatal(s)
		}
	}
	if _, e := os.Stat(directory); !os.IsNotExist(e) {
		t.Fatal("acquire/status performed file I/O", e)
	}
	empty := closeLease(t, l)
	if !empty.Durable || empty.ThroughAcceptedRecords != 0 {
		t.Fatal(empty)
	}
	<-l.Done()
	if _, e := os.Stat(directory); !os.IsNotExist(e) {
		t.Fatal("empty close created file", e)
	}
	l = lease(t, r, directory)
	l.Enqueue("one\n", false)
	if !closeLease(t, l).Durable {
		t.Fatal("not durable")
	}
	<-l.Done()
	for p, mode := range map[string]os.FileMode{directory: 0700, filepath.Join(directory, "runtime.jsonl"): 0600} {
		s, e := os.Stat(p)
		if e != nil {
			t.Fatal(e)
		}
		if s.Mode().Perm() != mode {
			t.Fatalf("%s mode %o", p, s.Mode().Perm())
		}
	}
	next := lease(t, r, directory)
	next.Enqueue("two\n", false)
	closeLease(t, next)
	<-next.Done()
	b, e := os.ReadFile(filepath.Join(directory, "runtime.jsonl"))
	if e != nil || string(b) != "one\ntwo\n" {
		t.Fatal(string(b), e)
	}
}
func TestActualFilePartialWriteAndFsyncFailure(t *testing.T) {
	for _, kind := range []string{"partial-success", "partial-error", "partial-eagain", "zero-write", "sync-error", "close-error"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			var writes, syncs, closes int
			var fd *os.File
			output := &fileSink{directory: dir, rotateBytes: 2 * 1024 * 1024}
			output.writeFile = func(f *os.File, p []byte) (int, error) {
				fd = f
				writes++
				if kind == "zero-write" {
					return 0, nil
				}
				if writes == 1 {
					n, e := f.Write(p[:min(2, len(p))])
					if e != nil {
						return n, e
					}
					if kind == "partial-error" {
						return n, syscall.EIO
					}
					if kind == "partial-eagain" {
						return n, syscall.EAGAIN
					}
					return n, nil
				}
				return f.Write(p)
			}
			output.syncFile = func(f *os.File) error {
				syncs++
				if e := f.Sync(); e != nil {
					return e
				}
				if kind == "sync-error" {
					return errors.New("private/path: sync failure")
				}
				return nil
			}
			output.closeFile = func(f *os.File) error {
				closes++
				if e := f.Close(); e != nil {
					return e
				}
				if kind == "close-error" {
					return errors.New("private/path: close failure")
				}
				return nil
			}
			c, g := gated(t, Options{Directory: dir}, output)
			c.Enqueue("first\n", false)
			c.Enqueue("second\n", false)
			d := beginFlush(t, c, DefaultTimeout, true)
			close(g)
			v := answer(t, d)
			<-c.Done()
			if syncs != 1 || closes != 1 {
				t.Fatal("not actual single fsync/close", syncs, closes)
			}
			if fd != nil {
				if _, e := fd.Write([]byte("bad")); !errors.Is(e, os.ErrClosed) {
					t.Fatal("fd still writable", e)
				}
			}
			bytes, e := os.ReadFile(filepath.Join(dir, "runtime.jsonl"))
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "partial-success":
				if !v.Durable || v.Status.WrittenRecords != 2 || string(bytes) != "first\nsecond\n" || writes != 3 {
					t.Fatal(v, string(bytes), writes)
				}
			case "sync-error", "close-error":
				if v.Durable || v.Complete || v.Status.FailedRecords != 0 || v.Status.WrittenRecords != 2 || v.FlushedAcceptedRecords != 0 || *v.Status.LastFailure != "sink_write_failed" {
					t.Fatal(v)
				}
			default:
				if v.Complete || v.Status.FailedRecords != 2 || v.Status.WrittenRecords != 0 || writes != 1 {
					t.Fatal(v, writes)
				}
				want := "fi"
				if kind == "zero-write" {
					want = ""
				}
				if string(bytes) != want {
					t.Fatal("partial prefix replayed", string(bytes))
				}
			}
		})
	}
}
func TestFsyncAndCloseArePhysicalOwnershipBoundaries(t *testing.T) {
	for _, operation := range []string{"fsync", "close"} {
		t.Run(operation, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "logs")
			entered := make(chan struct{})
			release := make(chan struct{})
			r := NewRegistry(io.Discard)
			var once sync.Once
			created := 0
			r.factory = func(o channelOptions, exit func()) (*Channel, error) {
				created++
				s := &fileSink{directory: o.Directory, rotateBytes: o.RotateBytes}
				block := func() { once.Do(func() { close(entered); <-release }) }
				if created == 1 {
					if operation == "fsync" {
						s.syncFile = func(f *os.File) error { block(); return f.Sync() }
					} else {
						s.closeFile = func(f *os.File) error { block(); return f.Close() }
					}
				}
				return startChannel(o, s, nil, exit), nil
			}
			a := lease(t, r, directory)
			a.Enqueue("one\n", false)
			answerCh := make(chan flushAnswer, 1)
			go func() {
				v, e := a.CloseWithTimeout(context.Background(), 20*time.Millisecond)
				answerCh <- flushAnswer{v, e}
			}()
			<-entered
			v := answer(t, answerCh)
			if v.Complete || v.Status.WrittenRecords != 1 || v.Status.FailedRecords != 0 || *v.Status.LastFailure != "flush_timeout" {
				t.Fatal(v)
			}
			select {
			case <-a.Done():
				t.Fatal("premature physical exit")
			default:
			}
			b := lease(t, r, directory)
			if b.owner != a.owner || b.Enqueue("blocked\n", false) || created != 1 {
				t.Fatal("premature replacement")
			}
			close(release)
			<-a.Done()
			if !b.Enqueue("two\n", false) || created != 2 {
				t.Fatal("replacement unavailable")
			}
			end := closeLease(t, b)
			<-b.Done()
			if end.Complete || end.Status.WrittenRecords != 2 || end.Status.FailedRecords != 0 {
				t.Fatal(end)
			}
			bytes, e := os.ReadFile(filepath.Join(directory, "runtime.jsonl"))
			if e != nil || string(bytes) != "one\ntwo\n" {
				t.Fatal(string(bytes), e)
			}
		})
	}
}

type announcingWriter struct {
	file    *os.File
	entered chan struct{}
	once    sync.Once
}

func (w *announcingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	return w.file.Write(p)
}
func TestInjectedBlockingWriterRetainsPhysicalTailAndBorrowedFD(t *testing.T) {
	read, write, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer read.Close()
	defer write.Close()
	entered := make(chan struct{})
	r := NewRegistry(&announcingWriter{file: write, entered: entered})
	out := lease(t, r, "")
	payload := strings.Repeat("x", 256*1024) + "\n"
	out.Enqueue(payload, false)
	<-entered
	// Receiving this prefix proves the worker reached the real pipe. The rest is
	// much larger than pipe capacity, so the record cannot settle without a reader.
	prefix := make([]byte, 1)
	if _, e = io.ReadFull(read, prefix); e != nil {
		t.Fatal(e)
	}
	result, e := out.CloseWithTimeout(context.Background(), 5*time.Millisecond)
	if e != nil || result.Complete || result.Status.UnconfirmedRecords != 1 {
		t.Fatal(result, e)
	}
	select {
	case <-out.Done():
		t.Fatal("closed a live blocked write")
	default:
	}
	file := lease(t, r, filepath.Join(t.TempDir(), "logs"))
	file.Enqueue("independent\n", false)
	if !closeLease(t, file).Durable {
		t.Fatal("file held by stdout")
	}
	remaining := make([]byte, len(payload)-1)
	if _, e = io.ReadFull(read, remaining); e != nil {
		t.Fatal(e)
	}
	<-out.Done()
	if string(prefix)+string(remaining) != payload {
		t.Fatal("pipe record split/replayed")
	}
	s := out.Status()
	if s.WrittenRecords != 1 || s.FailedRecords != 0 || s.UnconfirmedRecords != 0 {
		t.Fatal(s)
	}
	// The physically finished channel still does not own the process's stdout.
	done := make(chan error, 1)
	go func() { _, e := write.Write([]byte("still-open")); done <- e }()
	last := make([]byte, 10)
	io.ReadFull(read, last)
	if e := <-done; e != nil || string(last) != "still-open" {
		t.Fatal(e, string(last))
	}
}
func TestStdoutNonzeroEAGAINIsTerminalAndBackoffCaps(t *testing.T) {
	t.Run("nonzero-error", func(t *testing.T) {
		s := &memorySink{steps: []step{{n: 2, err: syscall.EAGAIN}}}
		c, g := gated(t, Options{}, s)
		c.Enqueue("abcdef", false)
		c.Enqueue("next", false)
		d := beginFlush(t, c, DefaultTimeout, true)
		close(g)
		v := answer(t, d)
		if v.Status.FailedRecords != 2 || s.text() != "ab" || s.writes != 1 {
			t.Fatal(v, s.text(), s.writes)
		}
	})
	t.Run("cap-reset", func(t *testing.T) {
		s := &memorySink{}
		for i := 0; i < 7; i++ {
			s.steps = append(s.steps, step{err: syscall.EAGAIN})
		}
		s.steps = append(s.steps, step{n: 1}, step{err: syscall.EAGAIN})
		c, g := gated(t, Options{}, s)
		var waits []time.Duration
		c.retryWait = func(d time.Duration, _ <-chan struct{}) bool { waits = append(waits, d); return true }
		c.Enqueue("ab", false)
		d := beginFlush(t, c, DefaultTimeout, true)
		close(g)
		v := answer(t, d)
		want := []time.Duration{2, 4, 8, 16, 32, 32, 32, 2}
		for i := range want {
			want[i] *= time.Millisecond
		}
		if !v.Complete || !reflect.DeepEqual(waits, want) {
			t.Fatal(v, waits)
		}
	})
}
func TestProcessWaitsForLogOwners(t *testing.T) {
	if directory := os.Getenv("HE_LOGDELIVERY_CHILD_DIRECTORY"); directory != "" {
		file, e := Acquire(directory)
		if e != nil {
			os.Exit(2)
		}
		out, e := Acquire("")
		if e != nil {
			os.Exit(3)
		}
		file.Enqueue("file\n", false)
		out.Enqueue("out\n", false)
		f, e := file.Close(context.Background())
		if e != nil || !f.Durable {
			os.Exit(4)
		}
		o, e := out.Close(context.Background())
		if e != nil || !o.Complete || o.Durable {
			os.Exit(5)
		}
		<-file.Done()
		<-out.Done()
		fmt.Print("closed\n")
		os.Exit(0)
	}
	directory := filepath.Join(t.TempDir(), "logs")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessWaitsForLogOwners$")
	child.Env = append(os.Environ(), "HE_LOGDELIVERY_CHILD_DIRECTORY="+directory)
	out, e := child.CombinedOutput()
	if e != nil || string(out) != "out\nclosed\n" {
		t.Fatal(e, string(out))
	}
	bytes, e := os.ReadFile(filepath.Join(directory, "runtime.jsonl"))
	if e != nil || string(bytes) != "file\n" {
		t.Fatal(string(bytes), e)
	}
}

func TestHeldActualFileWriteTimeoutRetainsCanonicalOwner(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "logs")
	alias := filepath.Join(root, "alias")
	if e := os.Symlink(root, alias); e != nil {
		t.Fatal(e)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	r := NewRegistry(io.Discard)
	created := 0
	var heldFD *os.File
	r.factory = func(o channelOptions, exit func()) (*Channel, error) {
		created++
		s := &fileSink{directory: o.Directory, rotateBytes: o.RotateBytes}
		if created == 1 {
			s.writeFile = func(f *os.File, p []byte) (int, error) {
				heldFD = f
				n, e := f.Write(p[:2])
				close(entered)
				<-release
				return n, e
			}
		}
		return startChannel(o, s, nil, exit), nil
	}
	a := lease(t, r, directory)
	a.Enqueue("old\n", false)
	<-entered
	timed, e := a.CloseWithTimeout(context.Background(), time.Millisecond)
	if e != nil || timed.Status.UnconfirmedRecords != 1 || timed.Status.WrittenRecords != 0 || timed.Status.FailedRecords != 0 {
		t.Fatal(timed, e)
	}
	b := lease(t, r, filepath.Join(alias, "logs"))
	if b.owner != a.owner || b.Enqueue("blocked\n", false) || created != 1 {
		t.Fatal("canonical overlap before actual write returned")
	}
	select {
	case <-a.Done():
		t.Fatal("Done before physical write completion")
	default:
	}
	close(release)
	<-a.Done()
	if _, e := heldFD.Stat(); !errors.Is(e, os.ErrClosed) {
		t.Fatal("Done before descriptor close", e)
	}
	retired := a.Status()
	if retired.FailedRecords != 1 || retired.UnconfirmedRecords != 0 || retired.PendingRecords != 0 {
		t.Fatal("late physical settlement not visible", retired)
	}
	if !b.Enqueue("new\n", false) || created != 2 {
		t.Fatal("no successor after physical close")
	}
	result := closeLease(t, b)
	<-b.Done()
	if result.Complete || result.Status.AcceptedRecords != 2 || result.Status.WrittenRecords != 1 || result.Status.FailedRecords != 1 {
		t.Fatal(result)
	}
	current, e := os.ReadFile(filepath.Join(directory, "runtime.jsonl"))
	if e != nil || string(current) != "new\n" {
		t.Fatal(string(current), e)
	}
	old, e := os.ReadFile(filepath.Join(directory, "runtime.jsonl.1"))
	if e != nil || string(old) != "ol" {
		t.Fatal("partial record not preserved on successor open", string(old), e)
	}
}
