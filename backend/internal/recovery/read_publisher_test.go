package recovery

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestReadPublisherIndependentMainWriterAndRevocationFence(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	reader, err := NewReadPublisher(ctx, f.main, f.authority)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.CloseAndWait(ctx)
	writer, err := f.main.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err = writer.ExecContext(ctx, "BEGIN IMMEDIATE; UPDATE data SET value='held'"); err != nil {
		t.Fatal(err)
	}
	// The only application pool connection is held by an uncommitted writer.
	// A deadline only bounds this test; production has no added timeout.
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var got string
	err = reader.Publish(readCtx, func(c *sql.Conn) ([]byte, error) {
		var v string
		e := c.QueryRowContext(readCtx, "SELECT value FROM data").Scan(&v)
		return []byte(v), e
	}, func(b []byte) error { got = string(b); return nil })
	if err != nil || got != "original" {
		t.Fatalf("independent WAL read: %q %v", got, err)
	}
	if _, err = writer.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	// An actual committed authority change must not reuse the prior read's fence.
	if _, err = f.authority.Write(ctx, f.main, []Revocation{Source{"scene", "revoked"}}, 1, noMutation); err != nil {
		t.Fatal(err)
	}
	// Alter the gate through the real writer fixture: the next publication must
	// fail before acceptance, even though the in-memory diagnostic data is ready.
	if _, err = f.main.Exec("UPDATE recovery_gate SET value=json_set(value,'$.state','quarantine')"); err != nil {
		t.Fatal(err)
	}
	emitted := false
	err = reader.Publish(ctx, func(*sql.Conn) ([]byte, error) { return []byte("private"), nil }, func([]byte) error { emitted = true; return nil })
	if err == nil || emitted {
		t.Fatal("invalid current gate published", err)
	}
}

func TestReadPublisherCloseWaitsActualAcceptedProjection(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	reader, err := NewReadPublisher(ctx, f.main, f.authority)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	published := make(chan error, 1)
	go func() {
		published <- reader.Publish(ctx, func(*sql.Conn) ([]byte, error) { close(entered); <-release; return []byte("accepted"), nil }, func([]byte) error { return nil })
	}()
	<-entered
	// Seal synchronously, then verify existing physical work remains owned.
	reader.work.Seal()
	closed := make(chan error, 1)
	go func() { closed <- reader.CloseAndWait(ctx) }()
	select {
	case err := <-closed:
		t.Fatal("closed over accepted projection", err)
	default:
	}
	if err = reader.Publish(ctx, func(*sql.Conn) ([]byte, error) { t.Error("late read"); return nil, nil }, func([]byte) error { return nil }); err == nil {
		t.Fatal("late admission accepted")
	}
	close(release)
	if err = <-published; err != nil {
		t.Fatal(err)
	}
	if err = <-closed; err != nil {
		t.Fatal(err)
	}
}
