package memoryservice

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memorylexical"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"os"
	"path/filepath"
	"testing"
)

func TestLearningTargetsActualSourceAndWriter(t *testing.T) {
	if _, e := memorylexical.Words("synthetic probe"); e != nil {
		if errors.Is(e, nodeicu.ErrUnavailable) && os.Getenv("REQUIRE_ICU") == "" {
			t.Skip("pinned native ICU source segmentation required")
		}
		t.Fatal(e)
	}
	raw, e := os.ReadFile("../learningowner/testdata/source.json")
	if e != nil {
		t.Fatal(e)
	}
	decoded, e := jsjson.ParseBytes(raw)
	if e != nil {
		t.Fatal(e)
	}
	root := object(decoded)
	manifest := object(value(root, "sources"))
	for _, m := range manifest.Members() {
		b, e := os.ReadFile(filepath.Join("../../..", m.Key))
		if e != nil {
			t.Fatal(e)
		}
		if m.Value != fmt.Sprintf("%x", sha256.Sum256(b)) {
			t.Fatalf("source drift: %s", m.Key)
		}
	}
	for _, raw := range epArr(value(root, "cases")) {
		c := object(raw)
		t.Run(text(c, "name"), func(t *testing.T) {
			s, f := serviceSetup(t)
			var sequence int
			var name, path string
			if e := f.main.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &path); e != nil {
				t.Fatal(e)
			}
			if _, e := recovery.AuthorityPath(path, filepath.Join(filepath.Dir(filepath.Dir(path)), "authority")); e != nil {
				t.Fatal(e)
			}
			scene := epObj("id", "s", "adapterId", "adapter", "accountId", "account", "learningEnabled", value(c, "enabled"))
			wire, _ := jsjson.Stringify(scene)
			if _, e = f.main.Exec("UPDATE scenes SET value=? WHERE id='s'", wire); e != nil {
				t.Fatal(e)
			}
			for _, v := range epArr(value(c, "rows")) {
				record := object(v)
				scope := object(value(record, "scope"))
				key := "scene:" + text(scope, "sceneId")
				if text(scope, "kind") == "sharedPublicKnowledge" {
					key = "shared:public"
				}
				raw, _ := jsjson.Stringify(record)
				if _, e = f.main.Exec("INSERT INTO memories(id,scope_key,person_id,content,fingerprint,value) VALUES(?,?,?,?,?,?)", text(record, "id"), key, value(scope, "personId"), text(record, "content"), text(record, "id"), raw); e != nil {
					t.Fatal(e)
				}
			}
			if value(c, "recalled") == true {
				if _, e = f.main.Exec("INSERT INTO recall_tombstones VALUES('s','source','2026-01-01')"); e != nil {
					t.Fatal(e)
				}
			}
			objects := func(v any) []*Object {
				out := []*Object{}
				for _, x := range epArr(v) {
					out = append(out, object(x))
				}
				return out
			}
			got, e := s.ReconciliationTargets(context.Background(), object(value(c, "input")), objects(value(c, "candidates")), objects(value(c, "semantic")))
			if e != nil {
				t.Fatal(e)
			}
			targets := []any{}
			for _, x := range got.Targets {
				targets = append(targets, x)
			}
			actual := epObj("targets", targets, "coverage", got.Coverage, "limits", got.Limits)
			if !deepEqual(actual, value(c, "expected")) {
				a, _ := jsjson.Stringify(actual)
				b, _ := jsjson.Stringify(value(c, "expected"))
				t.Fatalf("got %s\nwant %s", a, b)
			}
		})
	}
}
