package credentials

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestActualSourceModelReadValueAndVisionCacheKey(t *testing.T) {
	raw, e := os.ReadFile("testdata/model-read-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixtures struct {
		Cases []struct {
			Name        string
			Input       json.RawMessage
			Present     bool
			Queries     []string
			Output, Key string
		}
	}
	if e = json.Unmarshal(raw, &fixtures); e != nil {
		t.Fatal(e)
	}
	for _, f := range fixtures.Cases {
		t.Run(f.Name, func(t *testing.T) {
			store := secretFixtureStore(t, true)
			if f.Present {
				if _, e = store.main.Exec("INSERT INTO secrets(namespace,name,ciphertext) VALUES('model','p','synthetic-never-decrypted')"); e != nil {
					t.Fatal(e)
				}
			}
			queries := []string{}
			var value *jsjson.Object
			e = store.readyRead(context.Background(), func(conn *sql.Conn) error {
				var err error
				value, err = ReadModelConfiguration(f.Input, func(namespace, name string) (bool, error) {
					queries = append(queries, name)
					var exists bool
					err := conn.QueryRowContext(context.Background(), "SELECT EXISTS(SELECT 1 FROM main.secrets WHERE namespace=? AND name=?)", namespace, name).Scan(&exists)
					return exists, err
				})
				return err
			})
			if e != nil {
				t.Fatal(e)
			}
			encoded, e := jsjson.Stringify(value)
			if e != nil {
				t.Fatal(e)
			}
			if encoded != f.Output {
				t.Fatalf("ordered source projection differs: %s source %s", encoded, f.Output)
			}
			if !reflect.DeepEqual(queries, f.Queries) {
				t.Fatal(queries, f.Queries)
			}
			key, e := jsjson.Stringify([]any{"visual-expression-v2", "s", "m", "a", value})
			if e != nil {
				t.Fatal(e)
			}
			if actual := fmt.Sprintf("%x", sha256.Sum256([]byte(key))); actual != f.Key {
				t.Fatal(actual, f.Key)
			}
		})
	}
}
