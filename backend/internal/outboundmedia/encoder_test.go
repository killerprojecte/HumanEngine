package outboundmedia

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func manager(t *testing.T) *Manager {
	t.Helper()
	m, e := NewManager(Config{AvailableMemory: func() uint64 { return 8 << 30 }})
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func payload(t *testing.T) *jsjson.Object {
	t.Helper()
	v, e := jsjson.Parse(`{"note":"😀\ud800<&","message":[{"type":"image","data":{"file":""}}]}`)
	if e != nil {
		t.Fatal(e)
	}
	return v.(*jsjson.Object)
}
func fixture(t *testing.T, n int) Source {
	t.Helper()
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i % 251)
	}
	path := filepath.Join(t.TempDir(), "original")
	if e := os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(data)
	return Source{Index: 0, Path: path, Bytes: int64(n), Hash: hex.EncodeToString(sum[:])}
}
func closeEncoder(t *testing.T, e *Encoder) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.CloseAndWait(ctx); err != nil {
		t.Fatal(err)
	}
}
func waitFor(t *testing.T, predicate func() bool) {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for !predicate() {
		if time.Now().After(until) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestActualSourceOracle(t *testing.T) {
	var oracle struct {
		Cases []struct {
			Size                        int
			Hash, WireHash, Name, Error string
			WireLength                  int
			Params                      json.RawMessage
			TextLength                  int
		}
	}
	raw, e := os.ReadFile("testdata/oracle.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &oracle); e != nil {
		t.Fatal(e)
	}
	m := manager(t)
	encoder := m.NewEncoder()
	defer closeEncoder(t, encoder)
	for _, c := range oracle.Cases {
		t.Run(c.Name+"/"+strconv.Itoa(c.Size), func(t *testing.T) {
			if c.Size > 0 {
				s := fixture(t, c.Size)
				if s.Hash != c.Hash {
					t.Fatal("source fixture mismatch")
				}
				value := payload(t)
				before, _ := jsjson.Stringify(value)
				lease, e := encoder.Encode(context.Background(), value, []Source{s})
				if e != nil {
					t.Fatal(e)
				}
				defer lease.Release()
				sum := sha256.Sum256(lease.Bytes)
				if hex.EncodeToString(sum[:]) != c.WireHash || len(lease.Bytes) != c.WireLength {
					t.Fatalf("wire differs from actual TypeScript oracle: length %d, hash %x", len(lease.Bytes), sum)
				}
				after, _ := jsjson.Stringify(value)
				if before != after {
					t.Fatal("caller envelope mutated")
				}
				return
			}
			if len(c.Params) > 0 {
				params, err := jsjson.Parse(string(c.Params))
				if err != nil {
					t.Fatal(err)
				}
				value := new(jsjson.Object)
				_ = value.Set("params", params)
				_ = value.Set("text", strings.Repeat("x", c.TextLength))
				inline, err := countInline(value)
				if err != nil {
					t.Fatal(err)
				}
				if (c.TextLength == 12000) != (inline > 64*1024) {
					t.Fatal("fixture did not exercise expected encoder path", inline)
				}
				lease, err := encoder.Encode(context.Background(), value, nil)
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(lease.Bytes)
				lease.Release()
				if hex.EncodeToString(sum[:]) != c.WireHash || len(lease.Bytes) != c.WireLength {
					t.Fatalf("empty-sources wire differs from actual source: length %d hash %x", len(lease.Bytes), sum)
				}
				if m.Usage().Jobs != 0 || m.Usage().ReservedBytes != 0 {
					t.Fatal("reservation leaked", m.Usage())
				}
				return
			}
			s := fixture(t, 3)
			v := payload(t)
			sources := []Source{s}
			switch c.Name {
			case "invalid-bytes":
				sources[0].Bytes = 0
			case "invalid-hash":
				sources[0].Hash = strings.Repeat("A", 64)
			case "inline-before-source":
				_ = v.Set("text", strings.Repeat("x", 180000))
				sources[0].Bytes = 0
			case "many-sources":
				sources = make([]Source, 65)
				for i := range sources {
					sources[i] = s
				}
			case "total-source":
				sources = []Source{s, s}
				sources[0].Bytes = MaxSource
			case "duplicate-index":
				sources = []Source{s, s}
			case "hash-mismatch":
				sources[0].Hash = strings.Repeat("0", 64)
			case "missing-path":
				sources[0].Path += "-missing"
			default:
				t.Fatalf("unknown oracle case %s", c.Name)
			}
			lease, e := encoder.Encode(context.Background(), v, sources)
			if lease != nil {
				lease.Release()
			}
			if e == nil || e.Error() != c.Error {
				t.Fatalf("got %v want %s", e, c.Error)
			}
		})
	}
}
func TestGlobalAdmissionAndPhysicalLease(t *testing.T) {
	m := manager(t)
	a, b := m.NewEncoder(), m.NewEncoder()
	defer closeEncoder(t, a)
	defer closeEncoder(t, b)
	var leases []*Lease
	for i := 0; i < 8; i++ {
		l, e := a.Encode(context.Background(), payload(t), nil)
		if e != nil {
			t.Fatal(e)
		}
		leases = append(leases, l)
	}
	if _, e := b.Encode(context.Background(), payload(t), nil); e == nil || e.Error() != "outbound_encoding_busy" {
		t.Fatal(e)
	}
	hold, e := leases[0].Hold()
	if e != nil {
		t.Fatal(e)
	}
	leases[0].Release()
	leases[0].Release()
	if m.Usage().Jobs != 8 {
		t.Fatal("early release while physical writer holds bytes")
	}
	if e = hold.Close(); e != nil {
		t.Fatal(e)
	}
	_ = hold.Close()
	if m.Usage().Jobs != 7 {
		t.Fatal(m.Usage())
	}
	if _, e = leases[0].Hold(); e == nil {
		t.Fatal("reused released lease")
	}
	next, e := b.Encode(context.Background(), payload(t), nil)
	if e != nil {
		t.Fatal(e)
	}
	next.Release()
	for _, l := range leases {
		l.Release()
	}
	if m.Usage().Jobs != 0 || m.Usage().ReservedBytes != 0 {
		t.Fatal(m.Usage())
	}
}
func TestCancellationRetainsPhysicalWorkAndFIFO(t *testing.T) {
	m := manager(t)
	a, b := m.NewEncoder(), m.NewEncoder()
	defer closeEncoder(t, a)
	defer closeEncoder(t, b)
	entered, drain := make(chan struct{}), make(chan struct{})
	var once sync.Once
	source := fixture(t, 49153)
	source.Retain = func() (func(), error) { return func() { once.Do(func() { close(entered) }); <-drain }, nil }
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		l, e := a.Encode(ctx, payload(t), []Source{source})
		if l != nil {
			l.Release()
		}
		result <- e
	}()
	<-entered
	queuedCtx, queuedCancel := context.WithCancel(context.Background())
	queued := make(chan error, 1)
	go func() {
		l, e := b.Encode(queuedCtx, payload(t), []Source{fixture(t, 1)})
		if l != nil {
			l.Release()
		}
		queued <- e
	}()
	waitFor(t, func() bool { return m.Usage().Jobs == 2 })
	queuedCancel()
	if e := <-queued; e == nil || e.Error() != "request_cancelled" {
		t.Fatal(e)
	}
	cancel()
	if e := <-result; e == nil || e.Error() != "request_cancelled" {
		t.Fatal(e)
	}
	if m.Usage().Jobs != 1 {
		t.Fatal("active reservation released before physical drain", m.Usage())
	}
	deadline, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if e := a.CloseAndWait(deadline); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	barrier := make(chan *Lease, 1)
	go func() {
		l, e := b.Encode(context.Background(), payload(t), []Source{fixture(t, 2)})
		if e != nil {
			t.Error(e)
		}
		barrier <- l
	}()
	select {
	case <-barrier:
		t.Fatal("queue overtook blocked physical encoder")
	case <-time.After(10 * time.Millisecond):
	}
	close(drain)
	l := <-barrier
	if l == nil {
		t.Fatal("missing barrier lease")
	}
	l.Release()
	closeEncoder(t, a)
	if m.Usage().Jobs != 0 {
		t.Fatal(m.Usage())
	}
}
func TestFileChangesAndLimits(t *testing.T) {
	m := manager(t)
	e := m.NewEncoder()
	defer closeEncoder(t, e)
	s := fixture(t, 3)
	if err := os.WriteFile(s.Path, []byte{8, 8, 8}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Encode(context.Background(), payload(t), []Source{s}); err == nil || err.Error() != "outbound_encoding_failed" {
		t.Fatal(err)
	}
	s = fixture(t, 3)
	if err := os.Remove(s.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Encode(context.Background(), payload(t), []Source{s}); err == nil {
		t.Fatal("removed file accepted")
	}
	for _, size := range []int64{0, -1, MaxSource + 1} {
		s.Bytes = size
		if _, err := e.Encode(context.Background(), payload(t), []Source{s}); err == nil || err.Error() != "outbound_media_invalid" {
			t.Fatal(err)
		}
	}
	// A real maximum-size original, not just metadata: no newly introduced cap.
	s = fixture(t, int(MaxSource))
	l, err := e.Encode(context.Background(), payload(t), []Source{s})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Bytes) < int(MaxSource) {
		t.Fatal("wire truncated")
	}
	l.Release()
}
func TestInlineBoundariesAndErrorPriority(t *testing.T) {
	m := manager(t)
	e := m.NewEncoder()
	defer closeEncoder(t, e)
	v := new(jsjson.Object)
	array := make([]any, 4096)
	_ = v.Set("a", array)
	if _, err := e.Encode(context.Background(), v, nil); err == nil || err.Error() != "outbound_wire_too_large" {
		t.Fatal(err)
	}
	var nested any = nil
	for i := 0; i < 17; i++ {
		nested = []any{nested}
	}
	v = new(jsjson.Object)
	_ = v.Set("a", nested)
	if _, err := e.Encode(context.Background(), v, nil); err == nil || err.Error() != "outbound_wire_too_large" {
		t.Fatal(err)
	}
	v = new(jsjson.Object)
	_ = v.Set("a", jsjson.Undefined)
	if _, err := e.Encode(context.Background(), v, nil); err == nil || err.Error() != "invalid_payload" {
		t.Fatal(err)
	}
	low, err := NewManager(Config{CapacityMiB: 16, AvailableMemory: func() uint64 { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	le := low.NewEncoder()
	defer closeEncoder(t, le)
	if _, err := le.Encode(context.Background(), payload(t), nil); err == nil || err.Error() != "outbound_memory_unavailable" {
		t.Fatal(err)
	}
	s := fixture(t, 1)
	s.Bytes = MaxSource
	if _, err := le.Encode(context.Background(), payload(t), []Source{s}); err == nil || err.Error() != "outbound_encoding_busy" {
		t.Fatal("busy must precede host memory", err)
	}
}

func TestOwnerDisposalSharesFIFOAndBoundsDrainingWorkers(t *testing.T) {
	m := manager(t)
	type running struct {
		encoder *Encoder
		drain   chan struct{}
		result  chan error
	}
	start := func() running {
		encoder := m.NewEncoder()
		entered, drain := make(chan struct{}), make(chan struct{})
		result := make(chan error, 1)
		s := fixture(t, 1)
		s.Retain = func() (func(), error) { return func() { close(entered); <-drain }, nil }
		go func() {
			l, e := encoder.Encode(context.Background(), payload(t), []Source{s})
			if l != nil {
				l.Release()
			}
			result <- e
		}()
		<-entered
		encoder.Dispose()
		if e := <-result; e == nil || e.Error() != "request_cancelled" {
			t.Fatal(e)
		}
		return running{encoder, drain, result}
	}
	a, b := start(), start()
	if m.Usage().Workers != 2 || m.Usage().Jobs != 2 {
		t.Fatal(m.Usage())
	}
	third := m.NewEncoder()
	defer closeEncoder(t, third)
	if _, e := third.Encode(context.Background(), payload(t), []Source{fixture(t, 1)}); e == nil || e.Error() != "outbound_encoding_busy" {
		t.Fatal(e)
	}
	if m.Usage().Jobs != 2 {
		t.Fatal("worker admission leaked", m.Usage())
	}
	close(a.drain)
	close(b.drain)
	closeEncoder(t, a.encoder)
	closeEncoder(t, b.encoder)
	waitFor(t, func() bool { return m.Usage().Workers == 0 })
	l, e := third.Encode(context.Background(), payload(t), []Source{fixture(t, 1)})
	if e != nil {
		t.Fatal(e)
	}
	l.Release()
}
func TestSourceRetainDoesNotHideRemoval(t *testing.T) {
	m := manager(t)
	e := m.NewEncoder()
	defer closeEncoder(t, e)
	s := fixture(t, 1)
	released := false
	s.Retain = func() (func(), error) {
		if err := os.Remove(s.Path); err != nil {
			return nil, err
		}
		return func() { released = true }, nil
	}
	if _, err := e.Encode(context.Background(), payload(t), []Source{s}); err == nil || err.Error() != "outbound_encoding_failed" {
		t.Fatal(err)
	}
	if !released {
		t.Fatal("callback owner leaked")
	}
}
func TestStructuredClonePreservesAliasFailure(t *testing.T) {
	m := manager(t)
	e := m.NewEncoder()
	defer closeEncoder(t, e)
	v := payload(t)
	a, _ := v.Get("message")
	part := a.([]any)[0]
	_ = v.Set("message", []any{part, part})
	s := fixture(t, 1)
	s2 := s
	s2.Index = 1
	if _, err := e.Encode(context.Background(), v, []Source{s, s2}); err == nil || err.Error() != "outbound_encoding_failed" {
		t.Fatal(err)
	}
}
func TestEnvironmentCapacityNumberSemantics(t *testing.T) {
	for _, raw := range []string{"16", " 16 ", "0x10", "0b10000", "0o20", "1.6e1", "768", "+16", "16.0", "\ufeff16\ufeff"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("HUMANENGINE_OUTBOUND_MAX_RESERVED_MIB", raw)
			if _, e := NewManagerFromEnvironment(); e != nil {
				t.Fatal(e)
			}
		})
	}
	for _, raw := range []string{"", " ", "15", "769", "16.5", "1_6", "Infinity", "NaN", "-0x10", "\u008516"} {
		t.Run("invalid/"+raw, func(t *testing.T) {
			t.Setenv("HUMANENGINE_OUTBOUND_MAX_RESERVED_MIB", raw)
			if _, e := NewManagerFromEnvironment(); e == nil {
				t.Fatal("accepted invalid capacity")
			}
		})
	}
}
