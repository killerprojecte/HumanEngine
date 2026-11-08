package semantic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestActualSourceCandidateAdmissionAndAttempts(t *testing.T) {
	raw, e := os.ReadFile("testdata/owner-source.json")
	semMust(t, e)
	fixture, e := parseRequestObject(string(raw))
	semMust(t, e)
	for _, m := range scObject(scGet(fixture, "sources")).Members() {
		raw, e := os.ReadFile(filepath.Join("../../..", m.Key))
		semMust(t, e)
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != m.Value {
			t.Fatal("stale source", m.Key)
		}
	}
	for _, v := range scArr(scGet(fixture, "cases")) {
		scenario := scStr(scGet(v, "scenario"))
		t.Run(scenario, func(t *testing.T) {
			f := semSetup(t)
			views := scObject(scGet(v, "views"))
			partition, _ := privacyHash("partition")
			inputKey, _ := privacyHash("input")
			a := PrivacyAuthority{AssertReady: func() error { return nil }, Current: func(_ Context, ref *jsjson.Object) (*jsjson.Object, error) {
				return scObject(scGet(views, scStr(scGet(ref, "id")))), nil
			}, AnchorCurrent: func(Context, *jsjson.Object) (*jsjson.Object, error) { return nil, nil }, EpisodeAnchor: func(Context, *jsjson.Object, []any, string) (bool, error) { return false, nil }, PartitionKey: func(Context, any) (string, error) { return partition, nil }}
			id := 0
			now, _ := time.Parse(time.RFC3339Nano, "2026-01-02T03:04:05.006Z")
			p := AdmissionPolicy{Enabled: scGet(v, "enabled") == true, LearningEnabled: true, AutoSummary: scGet(v, "auto") == true, Now: func() time.Time { return now }, ID: func() (string, error) { id++; return fmt.Sprintf("candidate-%d", id), nil }}
			c := Context{SceneID: "scene"}
			parents, inputs := scArr(scGet(v, "parents")), scArr(scGet(v, "inputs"))
			var got any
			err := f.run(func(s *Store) error {
				if scenario == "attempt-missing" {
					return s.FinishAttempt(c, inputKey, "finished")
				}
				if scenario == "attempt" || scenario == "abandoned" {
					first, e := s.BeginAttempt(c, inputKey, parents, a, p)
					if e != nil {
						return e
					}
					second, e := s.BeginAttempt(c, inputKey, parents, a, p)
					if e != nil {
						return e
					}
					status := "finished"
					if scenario == "abandoned" {
						status = "abandoned"
					}
					if e = s.FinishAttempt(c, inputKey, status); e != nil {
						return e
					}
					if e = s.FinishAttempt(c, inputKey, "abandoned"); e != nil {
						return e
					}
					got = inputObject("first", first, "duplicate", second)
					return nil
				}
				first, e := s.Admit(c, parents, inputs, nil, a, p)
				if e != nil {
					return e
				}
				got = admissionValue(first)
				if scenario == "seen" {
					second, e := s.Admit(c, parents, inputs, nil, a, p)
					if e != nil {
						return e
					}
					got = inputObject("first", got, "second", admissionValue(second))
				}
				return nil
			})
			if scGet(v, "error") != nil {
				if err == nil || err.Error() != scGet(v, "error") {
					t.Fatal(err, scGet(v, "error"))
				}
			} else {
				semMust(t, err)
				if !privacyEqual(got, scGet(v, "result")) {
					raw, _ := jsjson.Stringify(got)
					want, _ := jsjson.Stringify(scGet(v, "result"))
					t.Fatalf("got %s want %s", raw, want)
				}
			}
			semMust(t, f.run(func(s *Store) error {
				rows, e := s.Rows(nil)
				if e != nil {
					return e
				}
				attempts, e := s.Attempts()
				if e != nil {
					return e
				}
				if !privacyEqual(objectsAny(rows), scGet(v, "rows")) || !privacyEqual(objectsAny(attempts), scGet(v, "attempts")) {
					t.Fatal("stored transition differs")
				}
				return nil
			}))
			_ = context.Background()
		})
	}
}
func objectsAny(v []*jsjson.Object) []any {
	out := []any{}
	for _, o := range v {
		out = append(out, o)
	}
	return out
}
func admissionValue(a AdmissionResult) *jsjson.Object {
	rejected := []any{}
	for _, r := range a.Rejected {
		rejected = append(rejected, inputObject("index", float64(r.Index), "reason", r.Reason))
	}
	return inputObject("created", objectsAny(a.Created), "rejected", rejected)
}
