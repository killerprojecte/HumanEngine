package logdelivery

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func traceFiles(directory, sourceDirectory string, operations *[][]any) func([]any) {
	return func(op []any) {
		for i, v := range op {
			if p, ok := v.(string); ok && strings.HasPrefix(p, directory) {
				op[i] = sourceDirectory + strings.TrimPrefix(p, directory)
			}
		}
		*operations = append(*operations, op)
	}
}
func TestSourcePhysicalOperationTraces(t *testing.T) {
	cases := sourceCases(t)
	for _, name := range []string{"file-open-failure-is-sink-failure-not-construction-failure", "sync-close-drains-existing-batch-and-fsync"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			if strings.HasPrefix(name, "file-open") {
				if e := os.Mkdir(filepath.Join(directory, "runtime.jsonl"), 0700); e != nil {
					t.Fatal(e)
				}
			}
			var operations [][]any
			s := &fileSink{directory: directory, rotateBytes: 2 * 1024 * 1024, trace: traceFiles(directory, "/logs", &operations)}
			c, g := gated(t, Options{Directory: directory}, s)
			c.Enqueue("one\n", false)
			c.Enqueue("two\n", false)
			d := beginFlush(t, c, DefaultTimeout, true)
			close(g)
			exact(t, cases, name, "result", answer(t, d))
			<-c.Done()
			exact(t, cases, name, "operations", operations)
		})
	}
	t.Run("retiring-owner-blocks-replacement-until-close-ack", func(t *testing.T) {
		name := "retiring-owner-blocks-replacement-until-close-ack"
		directory := t.TempDir()
		r := NewRegistry(io.Discard)
		start := make(chan struct{})
		finish := make(chan struct{})
		created := 0
		var operations [][]any
		r.factory = func(o channelOptions, exit func()) (*Channel, error) {
			created++
			var gate <-chan struct{}
			if created == 1 {
				gate = start
			}
			s := &fileSink{directory: directory, rotateBytes: o.RotateBytes, trace: traceFiles(directory, "/logs", &operations)}
			c := startChannel(o, s, gate, exit)
			c.flushStarted = make(chan struct{}, 16)
			if created == 1 {
				c.afterControl = func() { <-finish }
			}
			return c, nil
		}
		a := lease(t, r, directory)
		a.Enqueue("one\n", false)
		closing := beginClose(t, a, DefaultTimeout)
		b := lease(t, r, directory)
		exact(t, cases, name, "whileClosing", b.Enqueue("rejected\n", false))
		close(start)
		exact(t, cases, name, "firstClose", answer(t, closing))
		exact(t, cases, name, "next", b.Enqueue("three\n", false))
		exact(t, cases, name, "result", closeLease(t, b))
		<-b.Done()
		exact(t, cases, name, "retired", a.Status())
		close(finish)
		<-a.Done()
		exact(t, cases, name, "operations", operations)
	})
	t.Run("canonical-existing-parent-shares-not-lexical-path", func(t *testing.T) {
		name := "canonical-existing-parent-shares-not-lexical-path"
		root := t.TempDir()
		real := filepath.Join(root, "real")
		if e := os.Mkdir(real, 0700); e != nil {
			t.Fatal(e)
		}
		alias := filepath.Join(root, "alias")
		if e := os.Symlink(real, alias); e != nil {
			t.Fatal(e)
		}
		directory := filepath.Join(real, "new")
		r := NewRegistry(io.Discard)
		created := 0
		var operations [][]any
		r.factory = func(o channelOptions, exit func()) (*Channel, error) {
			created++
			return startChannel(o, &fileSink{directory: o.Directory, rotateBytes: o.RotateBytes, trace: traceFiles(directory, "/real/new", &operations)}, nil, exit), nil
		}
		a := lease(t, r, filepath.Join(alias, "new"))
		b := lease(t, r, directory)
		a.Enqueue("shared\n", false)
		closeLease(t, a)
		exact(t, cases, name, "result", closeLease(t, b))
		<-b.Done()
		exact(t, cases, name, "workers", created)
		exact(t, cases, name, "operations", operations)
	})
}
func TestSourceConcurrentFlushResamplesAfterWait(t *testing.T) {
	cases := sourceCases(t)
	name := "concurrent-flush-resamples-after-wait"
	directory := t.TempDir()
	var operations [][]any
	output := &fileSink{directory: directory, rotateBytes: 2 * 1024 * 1024, trace: traceFiles(directory, "/logs", &operations)}
	c, start := gated(t, Options{Directory: directory}, output)
	reached := make(chan struct{}, 2)
	resume := make(chan struct{})
	c.afterControl = func() { reached <- struct{}{}; <-resume }
	c.Enqueue("one\n", false)
	first := beginFlush(t, c, DefaultTimeout, false)
	c.Enqueue("two\n", false)
	second := beginFlush(t, c, DefaultTimeout, false)
	c.Enqueue("three\n", false)
	close(start)
	<-reached
	a := answer(t, first)
	resume <- struct{}{}
	<-reached
	b := answer(t, second)
	exact(t, cases, name, "results", []FlushResult{a, b})
	close(resume)
	exact(t, cases, name, "close", flush(t, c, true))
	<-c.Done()
	exact(t, cases, name, "operations", operations)
}
