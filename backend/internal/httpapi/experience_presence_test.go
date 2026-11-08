package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestActualExperiencePresenceAndExpiryCommitSource(t *testing.T) {
	testExperiencePresenceSource(t, "testdata/experience-presence-source.json")
}
func TestActualExperienceReviewFixSource(t *testing.T) {
	testExperiencePresenceSource(t, "testdata/experience-reviewfix-source.json")
}
func TestActualExperienceRootFixSource(t *testing.T) {
	testExperiencePresenceSource(t, "testdata/experience-rootfix-source.json")
}
func testExperiencePresenceSource(t *testing.T, path string) {
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var oracle struct {
		Cases []struct {
			Name      string
			Operation string
			NoTrial   bool
			Initial   map[string]json.RawMessage
			Responses []struct {
				Events                                 []string
				NormalizedHeadHash, NormalizedGateHash string
				Status                                 int
				Text                                   string
				HeadDelta, GateDelta, Updates          int64
				Policy                                 json.RawMessage
			}
		}
	}
	if e = json.Unmarshal(raw, &oracle); e != nil {
		t.Fatal(e)
	}
	for _, c := range oracle.Cases {
		t.Run(c.Name, func(t *testing.T) {
			s, db, a := experienceHTTP(t)
			receipt, present := c.Initial["receipt"]
			if !present {
				c.Initial["receipt"] = json.RawMessage(`null`)
			}
			seedExperienceHTTP(t, db, c.Initial)
			if !present {
				if _, err := db.Exec("DELETE FROM memory_experience_validations"); err != nil {
					t.Fatal(err)
				}
			} else {
				c.Initial["receipt"] = receipt
			}
			if c.Operation == "active" {
				if _, err := db.Exec("UPDATE memory_experience_policies SET version=2 WHERE id='p'"); err != nil {
					t.Fatal(err)
				}
			}
			if c.NoTrial {
				if _, e := db.Exec("DELETE FROM memory_experience_trials"); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = db.Exec("CREATE TABLE synthetic_updates(n);CREATE TRIGGER synthetic_count AFTER UPDATE ON memory_experience_policies BEGIN INSERT INTO synthetic_updates VALUES(1);END"); e != nil {
				t.Fatal(e)
			}
			gateBefore, e := recovery.ReadGate(context.Background(), db)
			if e != nil {
				t.Fatal(e)
			}
			ledger, e := sql.Open("sqlite3", filepath.Join(gateBefore.Authority, "authority.sqlite"))
			if e != nil {
				t.Fatal(e)
			}
			defer ledger.Close()
			before, e := a.Head(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			for index, want := range c.Responses {
				method, path, body := "GET", experiencePrefix+"?sceneId=s", ""
				if c.Operation == "validate" || c.Operation == "active" || c.Operation == "suspended" || c.Operation == "retired" {
					method = "POST"
					suffix := "review"
					value := map[string]any{"sceneId": "s", "expectedVersion": 1}
					if c.Operation == "active" {
						value["expectedVersion"] = 2
					}
					if c.Operation == "validate" {
						suffix = "validate"
					} else {
						value["status"] = c.Operation
					}
					path = experiencePrefix + "/p/" + suffix
					b, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					body = string(b)
				}
				r := httptest.NewRequest(method, path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Authorization", "Bearer "+fixtureToken)
				r.Header.Set("X-Request-ID", "<request-id>")
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code != want.Status {
					t.Fatalf("request%d got%d want%d: %s", index, w.Code, want.Status, w.Body.String())
				}
				equal := func(got, want []byte) {
					t.Helper()
					var av, bv any
					if e := json.Unmarshal(got, &av); e != nil {
						t.Fatal(e)
					}
					if e := json.Unmarshal(want, &bv); e != nil {
						t.Fatal(e)
					}
					if !reflect.DeepEqual(av, bv) {
						t.Fatalf("request%d JSON mismatch\ngot %s\nwant%s", index, got, want)
					}
				}
				equal(w.Body.Bytes(), []byte(want.Text))
				after, e := a.Head(context.Background())
				if e != nil {
					t.Fatal(e)
				}
				gate, e := recovery.ReadGate(context.Background(), db)
				if e != nil {
					t.Fatal(e)
				}
				var updates int64
				if e = db.QueryRow("SELECT count(*) FROM synthetic_updates").Scan(&updates); e != nil {
					t.Fatal(e)
				}
				if after.Sequence-before.Sequence != want.HeadDelta || gate.Head.Sequence-before.Sequence != want.GateDelta || updates != want.Updates {
					t.Fatalf("request%d head/gate/updates got%d/%d/%d want%d/%d/%d", index, after.Sequence-before.Sequence, gate.Head.Sequence-before.Sequence, updates, want.HeadDelta, want.GateDelta, want.Updates)
				}
				records, e := ledger.Query("SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence", before.Sequence)
				if e != nil {
					t.Fatal(e)
				}
				payloads := []string{}
				for records.Next() {
					var payload string
					if e = records.Scan(&payload); e != nil {
						t.Fatal(e)
					}
					payloads = append(payloads, payload)
				}
				if e = records.Err(); e != nil {
					t.Fatal(e)
				}
				records.Close()
				if !reflect.DeepEqual(payloads, want.Events) || after.Hash != want.NormalizedHeadHash || gate.Head.Hash != want.NormalizedGateHash {
					t.Fatalf("request%d durable bytes/chain mismatch\ngot %q %s %s\nwant%q %s %s", index, payloads, after.Hash, gate.Head.Hash, want.Events, want.NormalizedHeadHash, want.NormalizedGateHash)
				}
				var policy string
				if e = db.QueryRow("SELECT value FROM memory_experience_policies WHERE id='p'").Scan(&policy); e != nil {
					t.Fatal(e)
				}
				equal([]byte(policy), want.Policy)
			}
		})
	}
}
