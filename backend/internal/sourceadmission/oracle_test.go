package sourceadmission

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resultValue(r Result) *Object {
	v := obj("status", r.Status)
	if r.Status == "rejected" {
		_ = v.Set("reason", r.Reason)
		return v
	}
	_ = v.Set("candidate", r.Candidate)
	_ = v.Set("scope", r.Scope)
	_ = v.Set("proof", r.Proof)
	_ = v.Set("attribution", r.Attribution)
	_ = v.Set("parents", r.Parents)
	return v
}
func TestActualSourceOracle(t *testing.T) {
	raw, e := os.ReadFile("testdata/source-admission.json")
	if e != nil {
		t.Fatal(e)
	}
	var data struct {
		Node, Timezone string
		Sources        map[string]string
		Cases          []struct{ Op, Name, Args, Expected string }
	}
	if e = json.Unmarshal(raw, &data); e != nil {
		t.Fatal(e)
	}

	if data.Node != "v24.22.1" || data.Timezone != "UTC" {
		t.Fatalf("unexpected source runtime metadata: %q %q", data.Node, data.Timezone)
	}
	required := []string{
		"src/memory/write/source-admission.ts", "src/memory/write/subject-attribution.ts",
		"src/memory/knowledge/admission.ts", "src/memory/knowledge/facade.ts", "src/memory/knowledge/service.ts", "src/memory/knowledge/qualification.ts", "src/memory/knowledge/contracts.ts",
		"src/memory/learning/contracts.ts", "src/memory/learning/source-evidence.ts",
		"src/memory/domain/proofs.ts", "src/memory/domain/events.ts", "src/memory/domain/record-values.ts",
		"src/memory/read/assertion-authority.ts", "src/memory/read/domain-source-events.ts",
		"src/memory/read/bound-parents.ts", "src/memory/read/proof-parents.ts", "src/memory/read/provenance.ts", "src/memory/read/applicability.ts",
		"package-lock.json", "backend/tests/generate-source-admission-fixtures.mjs",
	}
	if len(data.Sources) != len(required) {
		t.Fatalf("oracle requires exactly %d source hashes, got %d", len(required), len(data.Sources))
	}
	for _, path := range required {
		want, present := data.Sources[path]
		if !present {
			t.Fatalf("oracle missing required hash: %s", path)
		}
		raw, e := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(path)))
		if e != nil {
			t.Fatal(e)
		}
		got := fmt.Sprintf("%x", sha256.Sum256(raw))
		if got != want {
			t.Fatalf("source drift %s: got %s want %s; regenerate actual-source oracle", path, got, want)
		}
	}
	for _, c := range data.Cases {
		t.Run(c.Op+"/"+c.Name, func(t *testing.T) {
			v, e := jsjson.Parse(c.Args)
			if e != nil {
				t.Fatal(e)
			}
			args := arr(v)
			var result any
			switch c.Op {
			case "prepare":
				r, err := PrepareKnowledge(object(args[0]), object(args[1]))
				e = err
				result = obj("ok", r.OK)
				if r.OK {
					_ = object(result).Set("context", r.Context)
					_ = object(result).Set("candidate", r.Candidate)
				} else {
					_ = object(result).Set("reason", r.Reason)
				}
			case "subject":
				result = ResolveSubject(object(args[0]), object(args[1]))
			case "evidence":
				messages := []*Object{}
				for _, m := range arr(args[0]) {
					messages = append(messages, object(m))
				}
				result, e = TrustedEvidence(messages, func(_, id string) (bool, error) {
					for _, r := range arr(args[1]) {
						if r == id {
							return true, nil
						}
					}
					return false, nil
				})

			case "parent-recall":
				f := scopedSetup(t)
				coordinator := parentCoordinator(t, f)
				ctx := context.Background()
				e = coordinator.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
					parent := object(args[1])
					input := object(args[3])
					candidate := object(args[4])
					if err := tx.Repository.Write(parent); err != nil {
						return err
					}
					if err := tx.Repository.SaveSupports("parent", arr(args[2])); err != nil {
						return err
					}
					if err := tx.Checkpoint(); err != nil {
						return err
					}
					for _, id := range arr(args[0]) {
						if err := tx.Store.WithWriteSession(func(session *recovery.WriteSession) error {
							_, err := session.Exec("INSERT INTO recall_tombstones VALUES('s',?,'2026-11-01T00:00:00Z')", id)
							return err
						}); err != nil {
							return err
						}
					}
					return tx.Store.Read(ctx, func(r *assertions.Reader) error {
						state, err := r.State("parent")
						if err != nil {
							return err
						}
						if state.Version != 1 || state.DeletionEpoch != 0 || state.Deleted {
							t.Fatalf("oracle exact reference mismatch: %+v", state)
						}
						authority, err := assertionauthority.New(r, testOptions(t))
						if err != nil {
							return err
						}
						valid, err := authority.ValidParent(object(get(parent, "scope")), "parent")
						if err != nil {
							return err
						}
						applicable, err := authority.CurrentApplicable(parent)
						if err != nil {
							return err
						}
						proof := obj("sources", get(candidate, "sources"), "parents", get(input, "dependencyMemoryIds"), "parentReferences", get(input, "dependencyParentReferences"))
						bound, err := authority.ProofParentsAvailable(object(get(parent, "scope")), proof, "current")
						if err != nil {
							return err
						}
						admission, err := New(r, testOptions(t))
						if err != nil {
							return err
						}
						prepared, err := admission.Prepare(input, candidate)
						if err != nil {
							return err
						}
						status := prepared.Reason
						if prepared.Status == "accepted" {
							status = prepared.Status
						}
						result = obj("validParent", valid, "currentApplicable", applicable, "bound", bound, "admission", status)
						return nil
					})
				})
			case "admit", "spans":
				f := scopedSetup(t)
				state := args[0]
				ctx := context.Background()
				e = f.recovery.WithWriteSession(ctx, f.main, func(session *recovery.WriteSession) error {
					account := "account"
					if truthy(get(state, "foreignIdentity")) {
						account = "other"
					}
					if truthy(get(state, "registered")) {
						identity := sourceevents.Identity{SceneID: "s", AdapterID: "adapter", AccountID: account, MessageID: "m", SenderID: "p"}
						_, err := f.events.WithSession(session).Observe(ctx, sourceevents.Change{Kind: "ingress", Identity: identity, BodyHash: strings.Repeat("1", 64)})
						if err != nil {
							return err
						}
						if truthy(get(state, "edited")) {
							_, err = f.events.WithSession(session).Observe(ctx, sourceevents.Change{Kind: "edit", Identity: identity, BodyHash: strings.Repeat("2", 64), EditID: "edit", Order: sourceevents.Order{Kind: "platform_sequence", Sequence: 1}})
							if err != nil {
								return err
							}
						}
					}
					if truthy(get(state, "recalled")) {
						if _, err := session.Exec("INSERT INTO recall_tombstones VALUES('s','m','2026-01-01T00:00:00Z')"); err != nil {
							return err
						}
					}
					if get(state, "enabled") == false {
						if _, err := session.Exec(`UPDATE scenes SET value='{"id":"s","adapterId":"adapter","accountId":"account","learningEnabled":false}'`); err != nil {
							return err
						}
					}
					return f.store.WithSession(session).Read(ctx, func(reader *assertions.Reader) error {
						a, err := New(reader, testOptions(t))
						if err != nil {
							return err
						}
						if c.Op == "spans" {
							spans, err := a.KnowledgeSpans(object(args[1]), object(args[2]), arr(args[3]))
							if err != nil {
								result = obj("error", err.Error())
							} else {
								result = obj("spans", spans)
							}
							return nil
						}
						r, err := a.Prepare(object(args[1]), object(args[2]))
						result = resultValue(r)
						return err
					})
				})
			default:
				t.Fatalf("unknown op %s", c.Op)
			}
			if e != nil {
				t.Fatal(e)
			}
			got, e := jsjson.Stringify(result)
			if e != nil {
				t.Fatal(e)
			}
			if got != c.Expected {
				t.Fatalf("got %s\nwant %s", got, c.Expected)
			}
		})
	}
}
