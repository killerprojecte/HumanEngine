package logdelivery

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Registry serializes identity and lease changes, never physical I/O. The
// process registry below is the application owner. NewRegistry permits an
// explicitly isolated owner, principally for tests and embedded applications.
type Registry struct {
	mu      sync.Mutex
	owners  map[string]*owner
	stdout  io.Writer
	factory func(channelOptions, func()) (*Channel, error) // package-private test boundary
}
type owner struct {
	channel    *Channel
	references int
	previous   *Status
	key        string
}

type Lease struct {
	registry                *Registry
	owner                   *owner
	released                bool
	retired                 *Channel
	previous                *Status
	rejected, rejectedBytes int64
}

func NewRegistry(stdout io.Writer) *Registry {
	if stdout == nil {
		stdout = os.Stdout
	}
	return &Registry{owners: make(map[string]*owner), stdout: stdout}
}

var processRegistry = NewRegistry(os.Stdout)

func Acquire(directory string) (*Lease, error) { return processRegistry.Acquire(directory) }

// CanonicalDirectory resolves the nearest existing ancestor without creating
// directories. Thus aliases of a not-yet-created child share the same owner.
func CanonicalDirectory(directory string) (string, error) {
	path, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			for i := len(missing) - 1; i >= 0; i-- {
				path = filepath.Join(path, missing[i])
			}
			return path, nil
		}
		missing = append(missing, filepath.Base(path))
		path = parent
	}
}
func (r *Registry) Acquire(directory string) (*Lease, error) {
	key := ""
	var err error
	if directory != "" {
		key, err = CanonicalDirectory(directory)
		if err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	shared := r.owners[key]
	if shared == nil {
		shared = &owner{key: key}
		ch, err := r.create(shared)
		if err != nil {
			return nil, err
		}
		shared.channel = ch
		r.owners[key] = shared
	}
	shared.references++
	return &Lease{registry: r, owner: shared}, nil
}
func (r *Registry) create(shared *owner) (*Channel, error) {
	options, _ := normalize(Options{Directory: shared.key})
	var channel *Channel
	exit := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if shared.references == 0 && shared.channel == channel && r.owners[shared.key] == shared {
			delete(r.owners, shared.key)
		}
	}
	var err error
	if r.factory != nil {
		channel, err = r.factory(options, exit)
	} else {
		var output sink = newStdoutSink(r.stdout)
		if shared.key != "" {
			output = &fileSink{directory: shared.key, rotateBytes: options.RotateBytes}
		}
		channel = startChannel(options, output, nil, exit)
	}
	return channel, err
}
func (l *Lease) Enqueue(record string, priority bool) bool {
	r := l.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	if l.released {
		l.rejected++
		l.rejectedBytes += encodedLength(record)
		return false
	}
	shared := l.owner
	if shared.channel.Closed() {
		// Status is observed once before replacement; retired handles retain their
		// own channel and history, never this new generation.
		previous := combined(shared.channel.Status(), shared.previous)
		ch, err := r.create(shared)
		if err != nil {
			return false
		} // defaults are validated at construction
		shared.previous = &previous
		shared.channel = ch
	}
	return shared.channel.Enqueue(record, priority)
}
func (l *Lease) statusLocked() Status {
	channel, previous := l.owner.channel, l.owner.previous
	if l.released {
		channel, previous = l.retired, l.previous
	}
	s := combined(channel.Status(), previous)
	if l.rejected > 0 {
		s.State = "degraded"
		s.RejectedRecords += l.rejected
		s.RejectedBytes += l.rejectedBytes
		if s.LastFailure == nil {
			reason := "lease_released"
			s.LastFailure = &reason
		}
	}
	return s
}
func (l *Lease) Status() Status {
	r := l.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	return l.statusLocked()
}

// Done refers to the lease's current or retired channel, not a future owner.
func (l *Lease) Done() <-chan struct{} {
	r := l.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	if l.released {
		return l.retired.Done()
	}
	return l.owner.channel.Done()
}
func (l *Lease) Close(ctx context.Context) (FlushResult, error) {
	return l.CloseWithTimeout(ctx, DefaultTimeout)
}
func (l *Lease) releaseLocked() (channel *Channel, last bool) {
	if !l.released {
		l.released = true
		l.retired = l.owner.channel
		l.previous = l.owner.previous
		l.owner.references--
		last = l.owner.references == 0
		// Fence admission before another acquire can observe this retiring owner.
		if last {
			l.retired.mu.Lock()
			l.retired.closing = true
			l.retired.mu.Unlock()
		}
	}
	return l.retired, last
}
func (l *Lease) CloseWithTimeout(ctx context.Context, timeout time.Duration) (FlushResult, error) {
	r := l.registry
	r.mu.Lock()
	channel, last := l.releaseLocked()
	r.mu.Unlock()
	return l.finishClose(ctx, timeout, channel, last)
}
func (l *Lease) finishClose(ctx context.Context, timeout time.Duration, channel *Channel, last bool) (FlushResult, error) {
	result, err := channel.Flush(ctx, timeout, last)
	r := l.registry
	r.mu.Lock()
	if l.owner.references == 0 && l.owner.channel.Closed() && r.owners[l.owner.key] == l.owner {
		delete(r.owners, l.owner.key)
	}
	result.Status = l.statusLocked()
	result.Complete = result.Complete && result.Status.LastFailure == nil
	result.Durable = result.Durable && result.Status.LastFailure == nil
	r.mu.Unlock()
	return result, err
}

// CloseSync is the synchronous Store.close adaptation. Repeated calls return
// nil, just like the source lease, while Close may observe a retired result.
func (l *Lease) CloseSync(timeout time.Duration) *FlushResult {
	r := l.registry
	r.mu.Lock()
	if l.released {
		r.mu.Unlock()
		return nil
	}
	channel, last := l.releaseLocked()
	r.mu.Unlock()
	result, _ := l.finishClose(context.Background(), timeout, channel, last)
	return &result
}
