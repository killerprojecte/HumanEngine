package providertransport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelcalls"
	_ "github.com/mattn/go-sqlite3"
)

func TestModelCallsActualSQLObserverRemainsInsideDrain(t *testing.T) {
	raw, err := os.ReadFile("../modelcalls/testdata/source-output.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct{ Schema string }
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "metadata.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(source.Schema); err != nil {
		t.Fatal(err)
	}
	owner := modelcalls.New(db)
	held, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	m := diagnosticModels(t, func(w http.ResponseWriter, _ *http.Request) { diagnosticResponse(w) })
	entered := make(chan struct{})
	m.Calls = callSinkFunc(func(ctx context.Context, e modelcalls.Observation) error { close(entered); return owner.Record(ctx, e) })
	returned := make(chan error, 1)
	go func() {
		value, err := m.Complete(context.Background(), diagnosticRequest())
		if value != nil {
			value.Release()
		}
		returned <- err
	}()
	select {
	case <-entered:
	case err := <-returned:
		t.Fatalf("model ended before actual metadata callback: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = m.CloseAndWait(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal("drain lost physical metadata SQL", err)
	}
	var count int
	if err = held.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM model_calls").Scan(&count); err != nil || count != 0 {
		t.Fatal("owner unavailable or premature metadata row", count, err)
	}
	if err = held.Close(); err != nil {
		t.Fatal(err)
	}
	if err = <-returned; err != nil {
		t.Fatal(err)
	}
	if err = m.CloseAndWait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM model_calls").Scan(&count); err != nil || count != 1 {
		t.Fatal("lost actual model-call row", count, err)
	}
}
