package credentials

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestModelCredentialReachedKeyCustody(t *testing.T) {
	for _, failure := range []string{"clear-sql", "later-key"} {
		t.Run(failure, func(t *testing.T) {
			s := secretFixtureStore(t, true)
			ctx := context.Background()
			var seq int
			var databaseName, path string
			rows, e := s.main.Query("PRAGMA database_list")
			if e != nil {
				t.Fatal(e)
			}
			for rows.Next() {
				if e = rows.Scan(&seq, &databaseName, &path); e != nil {
					t.Fatal(e)
				}
				if databaseName == "main" {
					break
				}
			}
			rows.Close()
			gate, e := recovery.ReadGate(ctx, s.main)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = recovery.AuthorityPath(path, gate.Authority); e != nil {
				t.Fatal(e)
			}
			old, _ := jsjson.Parse(`{"providers":[{"id":"p","name":"Synthetic","protocol":"openai-chat","baseUrl":"https://example.invalid"}],"models":[],"routes":[]}`)
			m, _ := jsjson.Parse(`{"p":{"action":"set","value":"synthetic-original"}}`)
			if _, e = s.SaveModels(ctx, old.(*jsjson.Object), m.(*jsjson.Object)); e != nil {
				t.Fatal(e)
			}
			before, e := s.authority.Head(ctx)
			if e != nil {
				t.Fatal(e)
			}
			key := filepath.Join(t.TempDir(), "synthetic.key")
			s.keyPath = key
			s.cipher = nil
			if failure == "clear-sql" {
				if _, e = s.main.Exec("CREATE TRIGGER fail_clear BEFORE DELETE ON secrets BEGIN SELECT RAISE(ABORT,'synthetic_clear_failure'); END"); e != nil {
					t.Fatal(e)
				}
			} else {
				if e = os.WriteFile(key, []byte("invalid-synthetic-key"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			next, _ := jsjson.Parse(`{"providers":[{"id":"q","name":"Synthetic","protocol":"openai-chat","baseUrl":"https://example.invalid"}],"models":[],"routes":[]}`)
			mutations, _ := jsjson.Parse(`{"q":{"action":"set","value":"synthetic-next"}}`)
			if _, e = s.SaveModels(ctx, next.(*jsjson.Object), mutations.(*jsjson.Object)); e == nil {
				t.Fatal("failure accepted")
			}
			after, e := s.authority.Head(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if after.Sequence != before.Sequence+1 {
				t.Fatalf("prefix %d want %d", after.Sequence, before.Sequence+1)
			}
			current, e := recovery.ReadGate(ctx, s.main)
			if e != nil || current.Head != before {
				t.Fatalf("gate changed on rollback: %v", e)
			}
			var count int
			if e = s.main.QueryRow("SELECT count(*) FROM secrets WHERE namespace='model' AND name='p'").Scan(&count); e != nil || count != 1 {
				t.Fatalf("clear escaped rollback %v", e)
			}
			if failure == "clear-sql" {
				if _, e = os.Stat(key); !os.IsNotExist(e) {
					t.Fatal("unreached key created", e)
				}
			}
		})
	}
}
