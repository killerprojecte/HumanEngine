package semantic

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// This differential test uses actual TS admitted storage and current authority
// fixtures. It does not claim a native application proposal or model round trip.
func TestSemanticModelActualSourceProjection(t *testing.T) {
	raw, e := os.ReadFile("testdata/model-source.json")
	semMust(t, e)
	parsed, e := jsjson.Parse(string(raw))
	semMust(t, e)
	for _, rawCase := range scArr(scGet(parsed, "cases")) {
		c := scObject(rawCase)
		t.Run(scStr(scGet(c, "scenario")), func(t *testing.T) {
			f := semSetup(t)
			events, e := sourceevents.New(f.main, f.authority, sourceevents.Options{})
			semMust(t, e)
			semMust(t, f.run(func(store *Store) error {
				for _, candidate := range scArr(scGet(c, "stored")) {
					if e := store.Write(scObject(candidate), nil, "admit"); e != nil {
						return e
					}
				}
				return nil
			}))
			var escaped *ModelReader
			check := func(reads botruntime.ReadAuthority) error {
				return events.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
					authority := PrivacyAuthority{AssertReady: scope.Ready, Current: func(_ Context, ref *jsjson.Object) (*jsjson.Object, error) {
						return scObject(scGet(scGet(c, "views"), scStr(scGet(ref, "id")))), nil
					}, AnchorCurrent: func(Context, *jsjson.Object) (*jsjson.Object, error) { return nil, errors.New("unexpected_anchor") }, PartitionKey: func(Context, any) (string, error) { return scStr(scGet(c, "partition")), nil }, EpisodeAnchor: func(Context, *jsjson.Object, []any, string) (bool, error) {
						return false, errors.New("unexpected_episode")
					}}
					policy := ModelReadPolicy{Authority: authority, Enabled: func(Context) (bool, error) { return scGet(c, "enabled") == true, nil }, NowMillis: func() float64 { return scGet(c, "now").(float64) }, ParseDate: func(raw string) (float64, error) {
						v, e := time.Parse(time.RFC3339Nano, raw)
						return float64(v.UnixMilli()), e
					}}
					if scGet(c, "scenario") == "current" {
						foreign := semSetup(t)
						if e := foreign.store.ReadRuntimeCurrent(scope, policy, func(*ModelReader) error { return nil }); e == nil {
							t.Fatal("foreign owner accepted runtime scope")
						}
					}
					return f.store.ReadRuntimeCurrent(scope, policy, func(reader *ModelReader) error {
						escaped = reader
						actual, e := reader.List(Context{SceneID: "s", Simulation: scGet(c, "simulation") == true}, int(scGet(c, "limit").(float64)))
						if e != nil {
							return e
						}
						if !privacyEqual(actual, scGet(c, "page")) {
							a, _ := jsjson.Stringify(actual)
							b, _ := jsjson.Stringify(scGet(c, "page"))
							t.Fatalf("actual=%s expected=%s", a, b)
						}
						if _, e = reader.List(Context{SceneID: "s"}, 41); e == nil {
							t.Fatal("invalid limit accepted")
						}
						return nil
					})
				})
			}
			semMust(t, events.WithRuntimeReads(context.Background(), check))
			if _, e = escaped.List(Context{SceneID: "s"}, 12); !errors.Is(e, sql.ErrTxDone) {
				t.Fatalf("expired reader: %v", e)
			}
			// Same reader in the actual active writer, max-open-connections=1. No
			// secondary Publish, pool expansion or goroutine-based workaround.
			semMust(t, f.authority.WithWriteSession(context.Background(), f.main, func(writer *recovery.WriteSession) error {
				return events.WithSession(writer).WithRuntimeReads(context.Background(), check)
			}))
		})
	}
}

func TestSemanticModelSchemaActualSource(t *testing.T) {
	raw, e := os.ReadFile("testdata/model-source.json")
	semMust(t, e)
	v, e := jsjson.Parse(string(raw))
	semMust(t, e)
	for _, entry := range scArr(scGet(v, "modelCases")) {
		t.Run(scStr(scGet(entry, "scenario")), func(t *testing.T) {
			out, e := ValidateModelEvidence(scObject(scGet(entry, "input")))
			want := scGet(entry, "success") == true
			if (e == nil) != want {
				t.Fatalf("source success=%v native error=%v", want, e)
			}
			if want && !privacyEqual(out, scGet(entry, "data")) {
				t.Fatal("source parsed value drift")
			}
		})
	}
}
