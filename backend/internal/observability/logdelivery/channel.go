package logdelivery

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

var ErrInvalidLimits = errors.New("invalid_log_queue_limits")
var errStopped = errors.New("delivery_stopped")
var errZeroWrite = errors.New("zero_write")

// Options uses nil for source defaults. Explicit zero is invalid for queue
// limits, but is permitted for RotateBytes, matching the source nullish defaults.
type Options struct {
	Directory   string
	MaxBytes    *int64
	MaxRecords  *int64
	RotateBytes *int64
}
type channelOptions struct {
	Directory   string
	MaxBytes    int64
	MaxRecords  int64
	RotateBytes int64
}

type sink interface {
	prepare() error
	write([]byte) (int, error)
	sync() error
	close() error
}

type control struct {
	through int64
	close   bool
	ack     chan struct{}
	done    chan struct{}
	acked   bool
}
type command struct {
	bytes   []byte
	barrier *control
}

// Channel admission never waits for an operating-system operation. One mutex
// protects counters and the bounded queue; the worker does all physical I/O
// after releasing it. No goroutine is created for an individual record.
type Channel struct {
	mu                                                    sync.Mutex
	options                                               channelOptions
	sink                                                  sink
	queue                                                 []command
	wake                                                  chan struct{}
	stop                                                  chan struct{}
	done                                                  chan struct{}
	accepted, written, failed, rejected, rejectedBytes    int64
	pendingBytes, pendingRecords                          int64
	flushed                                               int64
	failure                                               string
	sinkFailed, closing, abandoned, closed, backpressured bool
	barrier                                               *control
	onExit                                                func()
	// Only package tests use these deterministic native boundary seams.
	startGate      <-chan struct{}
	flushStarted   chan struct{}
	afterControl   func()
	retryWait      func(time.Duration, <-chan struct{}) bool
	encodeFailure  bool
	handoffFailure string
}

func normalize(options Options) (channelOptions, error) {
	value := channelOptions{Directory: options.Directory, MaxBytes: 1024 * 1024, MaxRecords: 256, RotateBytes: 2 * 1024 * 1024}
	if options.Directory != "" {
		value.MaxBytes = 4 * 1024 * 1024
		value.MaxRecords = 1024
	}
	if options.MaxBytes != nil {
		value.MaxBytes = *options.MaxBytes
	}
	if options.MaxRecords != nil {
		value.MaxRecords = *options.MaxRecords
	}
	if options.RotateBytes != nil {
		value.RotateBytes = *options.RotateBytes
	}
	if value.MaxBytes < 1024 || value.MaxBytes > 64*1024*1024 || value.MaxRecords < 8 || value.MaxRecords > 65536 {
		return channelOptions{}, ErrInvalidLimits
	}
	return value, nil
}

// NewChannel constructs a standalone source-compatible owner. A registry lease
// should be preferred whenever several Stores may share one file directory.
// stdout is borrowed and defaults to os.Stdout; no descriptor opens on creation.
func NewChannel(options Options, stdout io.Writer) (*Channel, error) {
	value, err := normalize(options)
	if err != nil {
		return nil, err
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	var output sink = newStdoutSink(stdout)
	if value.Directory != "" {
		output = &fileSink{directory: value.Directory, rotateBytes: value.RotateBytes}
	}
	return startChannel(value, output, nil, nil), nil
}
func newChannel(options Options, output sink, startGate <-chan struct{}, onExit func()) (*Channel, error) {
	value, err := normalize(options)
	if err != nil {
		return nil, err
	}
	return startChannel(value, output, startGate, onExit), nil
}
func startChannel(options channelOptions, output sink, startGate <-chan struct{}, onExit func()) *Channel {
	c := &Channel{options: options, sink: output, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}), startGate: startGate, onExit: onExit}
	go c.run()
	return c
}

// Enqueue accepts a canonical WTF-8 Go string, including the representation
// used by jsjson. Lone UTF-16 surrogates become one UTF-8 replacement scalar,
// exactly as Buffer.from(string, 'utf8'). It never splits or truncates a record.
func (c *Channel) Enqueue(record string, priority bool) bool {
	length := encodedLength(record)
	c.mu.Lock()
	defer c.mu.Unlock()
	reserveBytes, reserveRecords := c.options.MaxBytes/8, max(int64(1), c.options.MaxRecords/8)
	if priority {
		reserveBytes = 0
		reserveRecords = 0
	}
	reason := ""
	switch {
	case c.closing:
		reason = "sink_closed"
	case c.sinkFailed:
		reason = "sink_failed"
	case length > c.options.MaxBytes:
		reason = "record_too_large"
	case c.pendingBytes+length > c.options.MaxBytes-reserveBytes || c.pendingRecords >= c.options.MaxRecords-reserveRecords:
		reason = "queue_full"
	}
	if reason != "" {
		c.reject(length, reason)
		return false
	}
	c.pendingBytes += length
	c.pendingRecords++
	// Reservation precedes allocation. No user code runs in this section.
	data, ok := c.encode(record, int(length))
	if !ok {
		c.pendingBytes -= length
		c.pendingRecords--
		c.reject(length, "encode_failed")
		return false
	}
	c.accepted++
	c.queue = append(c.queue, command{bytes: data})
	c.signal()
	return true
}
func (c *Channel) reject(n int64, reason string) {
	c.rejected++
	c.rejectedBytes += n
	c.failure = reason
}
func (c *Channel) encode(record string, size int) (data []byte, ok bool) {
	if c.encodeFailure {
		return nil, false
	}
	defer func() {
		if recover() != nil {
			data = nil
			ok = false
		}
	}()
	data = make([]byte, size)
	offset := 0
	for i := 0; i < len(record); {
		n, replacement := nextUTF8(record[i:])
		if replacement {
			copy(data[offset:], "\xef\xbf\xbd")
			offset += 3
		} else {
			copy(data[offset:], record[i:i+n])
			offset += n
		}
		i += n
	}
	return data, offset == size
}
func nextUTF8(s string) (n int, replacement bool) {
	if len(s) >= 3 && s[0] == 0xed && s[1] >= 0xa0 && s[1] <= 0xbf && s[2] >= 0x80 && s[2] <= 0xbf {
		return 3, true
	}
	r, n := utf8.DecodeRuneInString(s)
	return n, r == utf8.RuneError && n == 1
}
func encodedLength(s string) int64 {
	var size int64
	for len(s) > 0 {
		n, bad := nextUTF8(s)
		if bad {
			size += 3
		} else {
			size += int64(n)
		}
		s = s[n:]
	}
	return size
}
func (c *Channel) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Channel) statusLocked() Status {
	s := Status{State: "ready", Backpressured: c.backpressured && !c.abandoned, AcceptedRecords: c.accepted, WrittenRecords: c.written, RejectedRecords: c.rejected, RejectedBytes: c.rejectedBytes, FailedRecords: c.failed, PendingRecords: c.pendingRecords, PendingBytes: c.pendingBytes, MaxPendingBytes: c.options.MaxBytes, MaxPendingRecords: c.options.MaxRecords}
	if c.closing {
		s.State = "closed"
	}
	reason := c.failure
	if reason == "" && c.sinkFailed {
		reason = "sink_write_failed"
	}
	if reason != "" {
		s.State = "degraded"
		s.LastFailure = &reason
	}
	if c.abandoned {
		s.PendingRecords = 0
		s.PendingBytes = 0
		s.UnconfirmedRecords = max(int64(0), c.accepted-c.written-c.failed)
	}
	return s
}
func (c *Channel) Status() Status { c.mu.Lock(); defer c.mu.Unlock(); return c.statusLocked() }
func (c *Channel) Closed() bool   { c.mu.Lock(); defer c.mu.Unlock(); return c.closed }

// Done closes only after the physical operation and descriptor cleanup return.
// Cancellation and timeout never close Done by themselves.
func (c *Channel) Done() <-chan struct{} { return c.done }
func (c *Channel) result(target int64) FlushResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resultLocked(target)
}
func (c *Channel) resultLocked(target int64) FlushResult {
	s := c.statusLocked()
	complete := c.flushed >= target && s.State != "degraded"
	return FlushResult{complete, complete && c.options.Directory != "", target, c.flushed, s}
}

// Flush captures a generation-local target and absolute deadline. There is
// only one admitted control barrier. A later caller waits for its finalization
// before sampling the then-current accepted count for its own next control.
// Its wait continuation is independent of caller cancellation, just as the
// source async operation continues independently of a detached consumer.
// Waiters are per flush, never per record or polling interval.
func (c *Channel) Flush(ctx context.Context, timeout time.Duration, closeSink bool) (FlushResult, error) {
	deadline := time.Now().Add(timeout)
	c.mu.Lock()
	target := c.accepted
	if closeSink {
		c.closing = true
	}
	pending, owns, finished := c.nextBarrierLocked(target, closeSink)
	c.mu.Unlock()
	if c.flushStarted != nil {
		select {
		case c.flushStarted <- struct{}{}:
		default:
		}
	}
	if finished {
		return c.result(target), nil
	}
	result := make(chan FlushResult, 1)
	go func() { result <- c.flushUntil(target, deadline, closeSink, pending, owns) }()
	select {
	case r := <-result:
		return r, nil
	case <-ctx.Done():
		return c.result(target), ctx.Err()
	}
}
func (c *Channel) nextBarrierLocked(target int64, closeSink bool) (*control, bool, bool) {
	if c.abandoned {
		return nil, false, true
	}
	if c.barrier != nil {
		return c.barrier, false, false
	}
	if c.closed || (!closeSink && c.flushed >= target) {
		return nil, false, true
	}
	pending := &control{through: c.accepted, close: closeSink, ack: make(chan struct{}), done: make(chan struct{})}
	c.barrier = pending
	if c.handoffFailure == "flush" {
		c.abandonLocked("flush_failed")
	} else {
		c.queue = append(c.queue, command{barrier: pending})
		c.signal()
	}
	return pending, true, false
}
func (c *Channel) flushUntil(target int64, deadline time.Time, closeSink bool, pending *control, owns bool) FlushResult {
	for {
		if owns {
			c.await(pending.ack, deadline)
			c.mu.Lock()
			if pending.acked && !c.sinkFailed {
				c.flushed = max(c.flushed, pending.through)
			} else if !pending.acked {
				c.abandonLocked("flush_timeout")
			}
			if c.barrier == pending {
				c.barrier = nil
			}
			// Finalize this source continuation before waking followers. A later
			// waiter must not make the initiating completed prefix wait again.
			result := c.resultLocked(target)
			close(pending.done)
			c.mu.Unlock()
			return result
		} else {
			if time.Until(deadline) <= 0 || !c.await(pending.done, deadline) {
				c.mu.Lock()
				c.abandonLocked("flush_timeout")
				c.mu.Unlock()
				return c.result(target)
			}
		}
		c.mu.Lock()
		var finished bool
		pending, owns, finished = c.nextBarrierLocked(target, closeSink)
		c.mu.Unlock()
		if finished {
			return c.result(target)
		}
	}
}
func (c *Channel) await(done <-chan struct{}, deadline time.Time) bool {
	// An already acknowledged own control can succeed at an expired deadline.
	select {
	case <-done:
		return true
	default:
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-c.stop:
		return false
	case <-timer.C:
		return false
	}
}
func (c *Channel) Close(ctx context.Context) (FlushResult, error) {
	return c.Flush(ctx, DefaultTimeout, true)
}

func (c *Channel) abandonLocked(reason string) {
	if c.failure == "" {
		c.failure = reason
	}
	if c.abandoned {
		return
	}
	c.abandoned = true
	c.closing = true
	c.queue = nil
	close(c.stop)
	c.signal()
}
func (c *Channel) run() {
	physicallyClosed := false
	defer func() {
		if recover() != nil {
			c.mu.Lock()
			c.abandonLocked("worker_failed")
			c.mu.Unlock()
		}
		// The descriptor is never closed by a cancelling caller or registry lock.
		var err error
		if !physicallyClosed {
			err = c.sink.close()
		}
		c.mu.Lock()
		if err != nil {
			c.sinkFailed = true
		}
		c.closed = true
		c.mu.Unlock()
		if c.onExit != nil {
			c.onExit()
		}
		close(c.done)
	}()
	if c.startGate != nil {
		select {
		case <-c.startGate:
		case <-c.stop:
			return
		}
	}
	for {
		c.mu.Lock()
		if c.abandoned {
			c.mu.Unlock()
			return
		}
		if len(c.queue) == 0 {
			c.mu.Unlock()
			select {
			case <-c.wake:
			case <-c.stop:
				return
			}
			continue
		}
		cmd := c.queue[0]
		c.queue[0] = command{}
		c.queue = c.queue[1:]
		if len(c.queue) == 0 {
			c.queue = nil
		}
		if cmd.barrier == nil && c.handoffFailure == "records" {
			c.abandonLocked("enqueue_failed")
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		if cmd.barrier != nil {
			b := cmd.barrier
			err := c.sink.sync()
			if b.close {
				if e := c.sink.close(); e != nil {
					err = e
				}
				physicallyClosed = true
			}
			c.mu.Lock()
			if err != nil {
				c.sinkFailed = true
			}
			if b.close {
				c.closed = true
			}
			b.acked = true
			close(b.ack)
			c.mu.Unlock()
			if c.afterControl != nil {
				c.afterControl()
			}
			if b.close {
				return
			}
		} else {
			c.writeRecord(cmd.bytes)
		}
	}
}
func (c *Channel) writeRecord(bytes []byte) {
	c.mu.Lock()
	failed := c.sinkFailed
	c.mu.Unlock()
	var err error
	if failed {
		err = errStopped
	} else {
		err = c.sink.prepare()
	}
	offset := 0
	backoff := 2 * time.Millisecond
	for err == nil && offset < len(bytes) {
		select {
		case <-c.stop:
			err = errStopped
			continue
		default:
		}
		n, writeErr := c.sink.write(bytes[offset:])
		// n>0 with an error is ambiguous, including EAGAIN. Never replay or retry it.
		if n < 0 || n > len(bytes)-offset {
			err = errZeroWrite
			continue
		}
		if n > 0 {
			offset += n
			backoff = 2 * time.Millisecond
			c.setBackpressure(false)
		}
		if writeErr != nil {
			if n == 0 && c.options.Directory == "" && (errors.Is(writeErr, syscall.EAGAIN) || errors.Is(writeErr, syscall.EWOULDBLOCK)) {
				c.setBackpressure(true)
				if !c.waitRetry(backoff) {
					err = errStopped
				}
				backoff = min(32*time.Millisecond, backoff*2)
			} else {
				err = writeErr
			}
		} else if n == 0 {
			err = errZeroWrite
		}
	}
	c.mu.Lock()
	if err != nil {
		c.sinkFailed = true
		c.failed++
	} else {
		c.written++
	}
	c.backpressured = false
	c.pendingRecords--
	c.pendingBytes -= int64(len(bytes))
	c.mu.Unlock()
}
func (c *Channel) setBackpressure(value bool) { c.mu.Lock(); c.backpressured = value; c.mu.Unlock() }
func (c *Channel) waitRetry(delay time.Duration) bool {
	if c.retryWait != nil {
		return c.retryWait(delay, c.stop)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-c.stop:
		return false
	}
}
