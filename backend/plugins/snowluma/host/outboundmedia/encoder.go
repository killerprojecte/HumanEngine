// Package outboundmedia implements SnowLuma's transport-owned outbound encoder.
// It does not resolve media, fetch remote content, or authorize a send.
package outboundmedia

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

const MiB int64 = 1024 * 1024
const MaxSource int64 = 64 * MiB
const MaxWire int64 = 96 * MiB

// Source describes an already resolved, authorized original file. Retain, when
// supplied, holds a callback owner, not the source file. It MUST NOT pin files,
// prevent removal/mutation, or defer revocation. Release runs after physical IO.
type Source struct {
	Index  int
	Path   string
	Bytes  int64
	Hash   string
	Retain func() (release func(), err error)
}
type Config struct {
	CapacityMiB     int64
	AvailableMemory func() uint64
}
type Usage struct {
	Jobs          int
	ReservedBytes int64
	Workers       int
}
type Manager struct {
	mu        sync.Mutex
	capacity  int64
	available func() uint64
	usage     Usage
	pool      *pool
}
type pool struct {
	manager  *Manager
	owners   int
	queue    []*job
	worker   bool
	disposed bool
	wake     chan struct{}
}
type Encoder struct {
	manager *Manager
	pool    *pool
	ctx     context.Context
	cancel  context.CancelFunc
	once    sync.Once
	mu      sync.Mutex
	closed  bool
	pending sync.WaitGroup
}
type job struct {
	ctx                         context.Context
	owner                       *Encoder
	value                       *jsjson.Object
	sources                     []Source
	result                      chan result
	active, cancelled, finished bool
	release                     func()
	releaseSources              func()
	done                        func()
}
type result struct {
	lease *Lease
	err   error
}

func NewManager(config Config) (*Manager, error) {
	if config.CapacityMiB == 0 {
		config.CapacityMiB = 768
	}
	if config.CapacityMiB < 16 || config.CapacityMiB > 768 {
		return nil, errors.New("invalid_outbound_memory_capacity")
	}
	if config.AvailableMemory == nil {
		config.AvailableMemory = availableMemory
	}
	return &Manager{capacity: config.CapacityMiB * MiB, available: config.AvailableMemory}, nil
}

// NewManagerFromEnvironment keeps the source's startup validation, including an
// explicitly empty environment value (Number(”) is zero, and therefore invalid).
func NewManagerFromEnvironment() (*Manager, error) {
	c := Config{CapacityMiB: 768}
	if raw, ok := os.LookupEnv("HUMANENGINE_OUTBOUND_MAX_RESERVED_MIB"); ok {
		v, err := capacityNumber(raw)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || v < 16 || v > 768 {
			return nil, errors.New("invalid_outbound_memory_capacity")
		}
		c.CapacityMiB = int64(v)
	}
	return NewManager(c)
}
func (m *Manager) Usage() Usage { m.mu.Lock(); defer m.mu.Unlock(); return m.usage }
func (m *Manager) NewEncoder() *Encoder {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pool == nil {
		m.pool = &pool{manager: m, wake: make(chan struct{}, 1)}
	}
	m.pool.owners++
	ctx, cancel := context.WithCancel(context.Background())
	return &Encoder{manager: m, pool: m.pool, ctx: ctx, cancel: cancel}
}
func (e *Encoder) Dispose() {
	e.once.Do(func() {
		e.mu.Lock()
		e.closed = true
		e.cancel()
		e.mu.Unlock()
		m := e.manager
		m.mu.Lock()
		p := e.pool
		p.owners--
		if p.owners == 0 {
			p.disposed = true
			if m.pool == p {
				m.pool = nil
			}
			p.notify()
		}
		m.mu.Unlock()
	})
}

// CloseAndWait cancels this owner and waits for its physical encoding work.
// Completed transport leases remain owned by their transports until Release.
func (e *Encoder) CloseAndWait(ctx context.Context) error {
	e.Dispose()
	done := make(chan struct{})
	go func() { e.pending.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *pool) notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (e *Encoder) Encode(ctx context.Context, value *jsjson.Object, sources []Source) (*Lease, error) {
	e.mu.Lock()
	if e.closed || ctx.Err() != nil {
		e.mu.Unlock()
		return nil, errors.New("request_cancelled")
	}
	e.pending.Add(1)
	e.mu.Unlock()
	done := e.pending.Done
	inline, err := countInline(value)
	if err != nil {
		done()
		return nil, err
	}
	total, err := validateSources(sources)
	if err != nil {
		done()
		return nil, err
	}
	charge := total*8 + inline*4 + MiB
	m := e.manager
	m.mu.Lock()
	if m.usage.Jobs >= 8 || m.usage.ReservedBytes+charge > m.capacity {
		m.mu.Unlock()
		done()
		return nil, errors.New("outbound_encoding_busy")
	}
	if uint64(m.usage.ReservedBytes+charge+128*MiB) > m.available() {
		m.mu.Unlock()
		done()
		return nil, errors.New("outbound_memory_unavailable")
	}
	m.usage.Jobs++
	m.usage.ReservedBytes += charge
	m.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() { m.mu.Lock(); m.usage.Jobs--; m.usage.ReservedBytes -= charge; m.mu.Unlock() })
	}
	fail := func(err error) (*Lease, error) { release(); done(); return nil, err }
	// Clone only the bounded envelope, never a base64 payload. Like structured
	// clone in the TS worker this prevents modifying the caller's message.
	cloned := clone(value).(*jsjson.Object)
	if len(sources) == 0 && inline <= 64*1024 {
		wire, err := jsjson.Marshal(cloned)
		if err != nil {
			return fail(err)
		}
		done()
		return newLease(wire, release), nil
	}
	sources = append([]Source(nil), sources...)
	holds := []func(){}
	releaseSources := func() {
		for i := len(holds) - 1; i >= 0; i-- {
			holds[i]()
		}
	}
	for _, source := range sources {
		if source.Retain != nil {
			hold, err := source.Retain()
			if err != nil {
				releaseSources()
				return fail(err)
			}
			if hold != nil {
				holds = append(holds, hold)
			}
		}
	}
	j := &job{ctx: ctx, owner: e, value: cloned, sources: sources, result: make(chan result, 1), release: release, releaseSources: releaseSources, done: done}
	p := e.pool
	m.mu.Lock()
	if e.ctx.Err() != nil || ctx.Err() != nil || p.disposed {
		m.mu.Unlock()
		releaseSources()
		return fail(errors.New("request_cancelled"))
	}
	if !p.worker {
		if m.usage.Workers >= 2 {
			m.mu.Unlock()
			releaseSources()
			return fail(errors.New("outbound_encoding_busy"))
		}
		p.worker = true
		m.usage.Workers++
		go p.run()
	}
	p.queue = append(p.queue, j)
	p.notify()
	m.mu.Unlock()
	cancel := func() {
		m.mu.Lock()
		if j.finished {
			m.mu.Unlock()
			return
		}
		j.cancelled = true
		if j.active {
			m.mu.Unlock()
			return
		}
		for i, v := range p.queue {
			if v == j {
				p.queue = append(p.queue[:i], p.queue[i+1:]...)
				break
			}
		}
		j.finished = true
		m.mu.Unlock()
		j.releaseSources()
		j.release()
		j.done()
	}
	select {
	case r := <-j.result:
		return r.lease, r.err
	case <-ctx.Done():
		cancel()
	case <-e.ctx.Done():
		cancel()
	}
	// A simultaneous completion may have transferred a lease into the result
	// channel. Reclaim it rather than dropping its reservation.
	m.mu.Lock()
	finished := j.finished
	m.mu.Unlock()
	if finished {
		select {
		case r := <-j.result:
			if r.lease != nil {
				r.lease.Release()
			}
		default:
		}
	}
	return nil, errors.New("request_cancelled")
}
func (p *pool) run() {
	m := p.manager

	for {
		m.mu.Lock()
		if len(p.queue) == 0 {
			if p.disposed {
				p.worker = false
				m.usage.Workers--
				m.mu.Unlock()
				return
			}
			m.mu.Unlock()
			timer := time.NewTimer(30 * time.Second)
			select {
			case <-p.wake:
				if !timer.Stop() {
					<-timer.C
				}
				continue
			case <-timer.C:
			}
			m.mu.Lock()
			if len(p.queue) == 0 { // retire atomically; a new submission can start a worker
				p.worker = false
				m.usage.Workers--
				m.mu.Unlock()
				return
			}
			m.mu.Unlock()
			continue
		}
		j := p.queue[0]
		p.queue = p.queue[1:]
		if j.ctx.Err() != nil || j.owner.ctx.Err() != nil || p.disposed {
			j.cancelled = true
			j.finished = true
			m.mu.Unlock()
			j.releaseSources()
			j.release()
			j.done()
			continue
		}
		j.active = true
		m.mu.Unlock()
		wire, err := encodeWire(j.value, j.sources)
		j.releaseSources()
		m.mu.Lock()
		j.finished = true
		if j.cancelled || j.ctx.Err() != nil || j.owner.ctx.Err() != nil {
			m.mu.Unlock()
			j.release()
		} else {
			if err != nil {
				m.mu.Unlock()
				j.release()
				j.result <- result{err: errors.New("outbound_encoding_failed")}
			} else {
				j.result <- result{lease: newLease(wire, j.release)}
				m.mu.Unlock()
			}
		}
		j.done()
	}
}

func validateSources(sources []Source) (int64, error) {
	if len(sources) > 64 {
		return 0, errors.New("outbound_media_too_large")
	}
	var total int64
	for _, s := range sources {
		n, err := jsjson.UTF16Len(s.Path)
		validHash := len(s.Hash) == 64
		for _, b := range []byte(s.Hash) {
			if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')) {
				validHash = false
				break
			}
		}
		if s.Bytes <= 0 || s.Bytes > MaxSource || !validHash || err != nil || n > 4096 {
			return 0, errors.New("outbound_media_invalid")
		}
		total += s.Bytes
	}
	if total > MaxSource {
		return 0, errors.New("outbound_media_too_large")
	}
	return total, nil
}
func countInline(value any) (int64, error) {
	var inline int64
	nodes := 0
	var count func(any, int) error
	count = func(v any, depth int) error {
		nodes++
		if nodes > 4096 || depth > 16 {
			return errors.New("outbound_wire_too_large")
		}
		switch x := v.(type) {
		case string:
			n, err := jsjson.UTF16Len(x)
			if err != nil {
				return errors.New("invalid_payload")
			}
			inline += int64(n)*6 + 2
		case nil, bool, float64:
			inline += 32
		case []any:
			for _, child := range x {
				if err := count(child, depth+1); err != nil {
					return err
				}
			}
		case *jsjson.Object:
			if x == nil {
				inline += 32
				break
			}
			for _, member := range x.Members() {
				n, err := jsjson.UTF16Len(member.Key)
				if err != nil {
					return errors.New("invalid_payload")
				}
				inline += int64(n)*6 + 4
				if err := count(member.Value, depth+1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid_payload")
		}
		if inline > MiB {
			return errors.New("outbound_wire_too_large")
		}
		return nil
	}
	err := count(value, 0)
	return inline, err
}

// Structured clone preserves shared object identity inside a bounded envelope.
func clone(v any) any {
	objects := map[*jsjson.Object]*jsjson.Object{}
	type arrayRef struct {
		pointer uintptr
		length  int
	}
	arrays := map[arrayRef][]any{}
	var visit func(any) any
	visit = func(v any) any {
		switch x := v.(type) {
		case *jsjson.Object:
			if x == nil {
				return (*jsjson.Object)(nil)
			}
			if cached, ok := objects[x]; ok {
				return cached
			}
			o := new(jsjson.Object)
			objects[x] = o
			for _, member := range x.Members() {
				_ = o.Set(member.Key, visit(member.Value))
			}
			return o
		case []any:
			key := arrayRef{reflect.ValueOf(x).Pointer(), len(x)}
			if cached, ok := arrays[key]; ok {
				return cached
			}
			a := make([]any, len(x))
			arrays[key] = a
			for i, item := range x {
				a[i] = visit(item)
			}
			return a
		default:
			return v
		}
	}
	return visit(v)
}

// Lease stays admitted until both the action settles (Release) and all physical
// writers (Hold) drain. Hold must be acquired before handing Bytes to transport.
type Lease struct {
	Bytes           []byte
	mu              sync.Mutex
	holds           int
	released, freed bool
	release         func()
}

func newLease(bytes []byte, release func()) *Lease { return &Lease{Bytes: bytes, release: release} }
func (l *Lease) Release()                          { l.mu.Lock(); l.released = true; l.free(); l.mu.Unlock() }
func (l *Lease) free() {
	if l.released && l.holds == 0 && !l.freed {
		l.freed = true
		l.release()
	}
}
func (l *Lease) Hold() (io.Closer, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil, fmt.Errorf("outbound_lease_released")
	}
	l.holds++
	return &leaseHold{lease: l}, nil
}

type leaseHold struct {
	lease *Lease
	once  sync.Once
}

func (h *leaseHold) Close() error {
	h.once.Do(func() { l := h.lease; l.mu.Lock(); l.holds--; l.free(); l.mu.Unlock() })
	return nil
}

var decimalNumber = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func capacityNumber(raw string) (float64, error) {
	raw = strings.TrimFunc(raw, func(r rune) bool { return r == '\uFEFF' || r != '\u0085' && unicode.IsSpace(r) })
	if len(raw) > 2 && raw[0] == '0' {
		base := 0
		switch raw[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			if strings.Contains(raw, "_") {
				return 0, errors.New("invalid capacity")
			}
			v, err := strconv.ParseUint(raw[2:], base, 64)
			return float64(v), err
		}
	}
	if !decimalNumber.MatchString(raw) {
		return 0, errors.New("invalid capacity")
	}
	return strconv.ParseFloat(raw, 64)
}

var processManager struct {
	once    sync.Once
	manager *Manager
	err     error
}

// NewEncoder uses the process-wide admission pool, shared across adapters.
func NewEncoder() (*Encoder, error) {
	processManager.once.Do(func() { processManager.manager, processManager.err = NewManagerFromEnvironment() })
	if processManager.err != nil {
		return nil, processManager.err
	}
	return processManager.manager.NewEncoder(), nil
}
