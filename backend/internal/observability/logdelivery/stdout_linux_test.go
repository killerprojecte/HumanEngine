//go:build linux

package logdelivery

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func pipeFlags(t *testing.T, file *os.File) (flags, capacity int) {
	t.Helper()
	raw, e := file.SyscallConn()
	if e != nil {
		t.Fatal(e)
	}
	var operationErr error
	e = raw.Control(func(fd uintptr) {
		flags, operationErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
		if operationErr == nil {
			capacity, operationErr = unix.FcntlInt(fd, unix.F_GETPIPE_SZ, 0)
		}
	})
	if e != nil {
		t.Fatal(e)
	}
	if operationErr != nil {
		t.Fatal(operationErr)
	}
	if flags&unix.O_NONBLOCK == 0 {
		t.Fatal("fixture requires an actually nonblocking pipe")
	}
	return
}
func TestActualStdoutNonblockingDescriptorBackpressureAndStop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		name := "resume-suffix"
		if stop {
			name = "stop-without-draining"
		}
		t.Run(name, func(t *testing.T) {
			reader, writer, e := os.Pipe()
			if e != nil {
				t.Fatal(e)
			}
			defer reader.Close()
			defer writer.Close()
			flags, capacity := pipeFlags(t, writer)
			payload := strings.Repeat("a", capacity) + strings.Repeat("b", capacity)
			registry := NewRegistry(writer)
			out := lease(t, registry, "")
			c := out.owner.channel
			reached := make(chan Status, 1)
			resume := make(chan struct{})
			c.retryWait = func(_ time.Duration, stopped <-chan struct{}) bool {
				select {
				case reached <- c.Status():
				default:
				}
				select {
				case <-resume:
					return true
				case <-stopped:
					return false
				}
			}
			if !out.Enqueue(payload, false) {
				t.Fatal("fixture exceeded source queue")
			}
			var held Status
			select {
			case held = <-reached:
			case <-time.After(3 * time.Second):
				t.Fatal("native EAGAIN was swallowed")
			}
			if !held.Backpressured || held.PendingRecords != 1 || held.PendingBytes != int64(len(payload)) || held.WrittenRecords != 0 || held.FailedRecords != 0 {
				t.Fatal(held)
			}
			// The real stdout fd remains backpressured while a separate actual file owner completes.
			file := lease(t, registry, filepath.Join(t.TempDir(), "logs"))
			file.Enqueue("independent\n", false)
			if !closeLease(t, file).Durable {
				t.Fatal("file blocked behind stdout")
			}
			<-file.Done()
			if stop {
				v, e := out.CloseWithTimeout(context.Background(), time.Millisecond)
				if e != nil || v.Complete || v.Status.LastFailure == nil || *v.Status.LastFailure != "flush_timeout" {
					t.Fatal(v, e)
				}
				// No bytes have been drained. Only an observable raw EAGAIN plus stop-aware
				// retry can finish here; Go File.Write's hidden wait would remain blocked.
				select {
				case <-out.Done():
				case <-time.After(time.Second):
					t.Fatal("stop required draining unwritten suffix")
				}
				s := out.Status()
				if s.WrittenRecords != 0 || s.FailedRecords != 1 || s.UnconfirmedRecords != 0 || s.Backpressured {
					t.Fatal(s)
				}
				prefix := make([]byte, capacity)
				if _, e := io.ReadFull(reader, prefix); e != nil || string(prefix) != strings.Repeat("a", capacity) {
					t.Fatal("partial prefix replayed", e)
				}
				raw, e := reader.SyscallConn()
				if e != nil {
					t.Fatal(e)
				}
				var n int
				var readErr error
				if e := raw.Control(func(fd uintptr) { n, readErr = syscall.Read(int(fd), make([]byte, 1)) }); e != nil {
					t.Fatal(e)
				}
				if n > 0 || !errors.Is(readErr, syscall.EAGAIN) {
					t.Fatal("stopped suffix was still written", n, readErr)
				}
			} else {
				prefix := make([]byte, capacity)
				if _, e := io.ReadFull(reader, prefix); e != nil {
					t.Fatal(e)
				}
				close(resume)
				suffix := make([]byte, capacity)
				if _, e := io.ReadFull(reader, suffix); e != nil {
					t.Fatal(e)
				}
				v := closeLease(t, out)
				<-out.Done()
				if !v.Complete || v.Durable || string(prefix)+string(suffix) != payload || v.Status.WrittenRecords != 1 || v.Status.FailedRecords != 0 {
					t.Fatal(v)
				}
			}
			after, _ := pipeFlags(t, writer)
			if after != flags {
				t.Fatal("borrowed fd flags changed", flags, after)
			}
			if n, e := writeFileDescriptor(writer, []byte("open")); e != nil || n != 4 {
				t.Fatal("borrowed fd was closed", n, e)
			}
			still := make([]byte, 4)
			if _, e := io.ReadFull(reader, still); e != nil || string(still) != "open" {
				t.Fatal(e, string(still))
			}
		})
	}
}
