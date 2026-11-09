package recovery

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
	"github.com/mattn/go-sqlite3"
)

// The test alone retains connection pointers long enough to observe actual
// physical Close through SQLiteConn.Ping. Production has no such registry.
type publisherInitTracker struct {
	mu             sync.Mutex
	connections    []*sqlite3.SQLiteConn
	made, released atomic.Int64
}
type publisherCallbackMarker struct {
	id      int64
	padding [64]byte
}

func (p *publisherInitTracker) initializer(fail bool) storage.ConnectionInitializer {
	return func(c *sqlite3.SQLiteConn) error {
		marker := &publisherCallbackMarker{id: p.made.Add(1)}
		runtime.SetFinalizer(marker, func(*publisherCallbackMarker) { p.released.Add(1) })
		p.mu.Lock()
		p.connections = append(p.connections, c)
		p.mu.Unlock()
		if e := c.RegisterFunc("publisher_lifetime_probe", func() int64 { defer runtime.KeepAlive(marker); return marker.id }, true); e != nil {
			return e
		}
		if fail {
			return errors.New("synthetic_initializer_failure")
		}
		return nil
	}
}
func (p *publisherInitTracker) observeClosedAndRelease(t *testing.T) int {
	t.Helper()
	p.mu.Lock()
	list := p.connections
	p.connections = nil
	p.mu.Unlock()
	seen := map[*sqlite3.SQLiteConn]bool{}
	for _, c := range list {
		if seen[c] {
			t.Fatal("initializer ran twice on one physical connection")
		}
		seen[c] = true
		if e := c.Ping(context.Background()); !errors.Is(e, driver.ErrBadConn) {
			t.Fatalf("physical connection not closed: %v", e)
		}
	}
	return len(list)
}
func (p *publisherInitTracker) awaitCallbackCollection(t *testing.T) {
	t.Helper()
	// Physical Close is independently verified above. Repeated collections and
	// an observed finalizer count prove no owner-held Go callback metadata remains;
	// no single GC timing or arbitrary sleep is treated as successful collection.
	deadline := time.Now().Add(10 * time.Second)
	for p.released.Load() != p.made.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("callbacks retained: made%d collected%d", p.made.Load(), p.released.Load())
		}
		runtime.GC()
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
}
func TestReadPublisherInitializerPhysicalChurn(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	tracker := new(publisherInitTracker)
	reader, e := NewReadPublisherWithInitializer(ctx, f.main, f.authority, tracker.initializer(false))
	if e != nil {
		t.Fatal(e)
	}
	defer reader.CloseAndWait(ctx)
	if _, e = reader.main.ExecContext(ctx, "UPDATE data SET value='forbidden'"); e == nil {
		t.Fatal("initialized read publisher allowed write")
	}
	var original string
	if e = f.main.QueryRowContext(ctx, "SELECT value FROM data").Scan(&original); e != nil || original != "original" {
		t.Fatal("writer state changed", original, e)
	}
	// Force churn in this test only. Production connection limits are untouched.
	reader.main.SetMaxIdleConns(0)
	for round := 0; round < 8; round++ {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				e := reader.Publish(ctx, func(c *sql.Conn) ([]byte, error) {
					var id int64
					e := c.QueryRowContext(ctx, "SELECT publisher_lifetime_probe()").Scan(&id)
					if e == nil && id <= 0 {
						e = errors.New("missing callback")
					}
					return []byte("ok"), e
				}, func(b []byte) error {
					if string(b) != "ok" {
						return errors.New("bad publication")
					}
					return nil
				})
				if e != nil {
					t.Error(e)
				}
			}()
		}
		wg.Wait()
	}
	if tracker.made.Load() != 65 {
		t.Fatalf("expected initial connection +64 physical reopens, got%d", tracker.made.Load())
	}
	if n := tracker.observeClosedAndRelease(t); n != 65 {
		t.Fatal("observed closures", n)
	}
	tracker.awaitCallbackCollection(t)
	// Reader remains alive and usable after collecting every old physical owner.
	if e = reader.Publish(ctx, func(c *sql.Conn) ([]byte, error) {
		var id int64
		e := c.QueryRowContext(ctx, "SELECT publisher_lifetime_probe()").Scan(&id)
		return nil, e
	}, func([]byte) error { return nil }); e != nil {
		t.Fatal(e)
	}
	if tracker.observeClosedAndRelease(t) != 1 {
		t.Fatal("reopen missing")
	}
	tracker.awaitCallbackCollection(t)
	runtime.KeepAlive(reader)
}
func TestReadPublisherInitializerFailureClosesPhysicalConnection(t *testing.T) {
	f := newWriterFixture(t)
	tracker := new(publisherInitTracker)
	reader, e := NewReadPublisherWithInitializer(context.Background(), f.main, f.authority, tracker.initializer(true))
	if e == nil || reader != nil {
		t.Fatal("initializer failure accepted", reader, e)
	}
	if tracker.observeClosedAndRelease(t) != 1 {
		t.Fatal("failed connection not observed")
	}
	tracker.awaitCallbackCollection(t)
	// Failure must not mutate or poison the original writer or normal readers.
	normal, e := NewReadPublisher(context.Background(), f.main, f.authority)
	if e != nil {
		t.Fatal(e)
	}
	defer normal.CloseAndWait(context.Background())
}
