package contextuallearning

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionproposal"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

type sourceFixture struct {
	Sources map[string]string
	Queues  []struct {
		Mode              string
		Result, Duplicate EnqueueResult
		Events            []Event
		Stats             Stats
	}
	Proposals []struct {
		Mode   string
		Result EnqueueResult
		Key    *string
		Writes []json.RawMessage
		Stats  Stats
	}
}

func sourceData(t *testing.T) sourceFixture {
	t.Helper()
	raw, e := os.ReadFile("testdata/source.json")
	if e != nil {
		t.Fatal(e)
	}
	var f sourceFixture
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	for path, hash := range f.Sources {
		raw, e := os.ReadFile(filepath.Join("../../..", path))
		if e != nil {
			t.Fatal(e)
		}
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != hash {
			t.Fatal("source hash changed", path)
		}
	}
	return f
}
func TestActualSourceContextualQueueOracle(t *testing.T) {
	for _, tc := range sourceData(t).Queues {
		t.Run(tc.Mode, func(t *testing.T) {
			var busy, valid atomic.Bool
			busy.Store(true)
			valid.Store(true)
			var mu sync.Mutex
			events := []Event{}
			q, _ := NewQueue(QueueOptions{ForegroundBusy: busy.Load, OnEvent: func(e Event) { mu.Lock(); events = append(events, e); mu.Unlock() }})
			defer q.CloseAndWait(context.Background())
			started, release := make(chan struct{}), make(chan struct{})
			j := Job{SceneID: "s", Key: tc.Mode, SourceIDs: []string{"b", "a", "a"}, Validate: valid.Load, Committed: func(v any) bool { return v == "saved" }, Run: func(context.Context) (any, error) {
				close(started)
				<-release
				if tc.Mode == "failed" {
					return nil, fmt.Errorf("PRIVATE_CANARY")
				}
				if tc.Mode == "discarded" || tc.Mode == "committed" {
					valid.Store(false)
				}
				if tc.Mode == "committed" || tc.Mode == "cancelled" {
					return "saved", nil
				}
				return nil, nil
			}}
			if got := q.Enqueue(j); got != tc.Result {
				t.Fatal(got, tc.Result)
			}
			j.SourceIDs = []string{"a", "b"}
			if got := q.Enqueue(j); got != tc.Duplicate {
				t.Fatal(got, tc.Duplicate)
			}
			busy.Store(false)
			q.Resume()
			<-started
			if tc.Mode == "cancelled" {
				q.Recall("s", "a")
			}
			close(release)
			waitIdle(t, q)
			if got := q.Stats(); got != tc.Stats {
				t.Fatal(got, tc.Stats)
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(events, tc.Events) {
				t.Fatal(events, tc.Events)
			}
		})
	}
}
func parseTest(t *testing.T, s string) *Object {
	t.Helper()
	v, e := jsjson.Parse(s)
	if e != nil {
		t.Fatal(e)
	}
	return v.(*Object)
}
func TestActualSourceContextualProposalOracle(t *testing.T) {
	for _, tc := range sourceData(t).Proposals {
		t.Run(tc.Mode, func(t *testing.T) {
			var busy, valid atomic.Bool
			busy.Store(true)
			valid.Store(true)
			writes := []*Object{}
			message := parseTest(t, `{"id":"a","sceneId":"s","senderId":"person","origin":"live","status":"available","sentAt":"2026-01-01T00:00:00.000Z","receivedAt":"2026-01-01T00:00:00.000Z","parts":[{"type":"text","text":"Synthetic durable fact"}]}`)
			p, e := NewProposals(Ports{State: func(context.Context, string, *recovery.WriteSession) (Settings, error) {
				return Settings{LearningEnabled: tc.Mode != "disabled", GraphEnabled: true, GraphAnswerEnabled: true, CommitmentsEnabled: true, KnowledgeEnabled: true}, nil
			}, Preview: func(string) ([]*Object, error) { return []*Object{message}, nil }, ForegroundBusy: busy.Load, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }, Registered: func(context.Context, string, string) (bool, error) {
				if tc.Mode == "preview-ref-registered-error" {
					t.Error("Registered must be short-circuited for a known source-event ref")
					return false, fmt.Errorf("REGISTERED_MUST_NOT_RUN")
				}
				return tc.Mode == "preview-registered", nil
			}, CurrentSource: func(context.Context, *Object, *recovery.WriteSession) (bool, error) {
				return tc.Mode != "source-changed" || valid.Load(), nil
			}, Recalled: func(context.Context, string, string, *recovery.WriteSession) (bool, error) {
				return tc.Mode == "source-changed" && !valid.Load(), nil
			}, MemoryStates: func(context.Context, string, []string, *recovery.WriteSession) (map[string]MemoryState, error) {
				return map[string]MemoryState{"parent": {Version: 1, Visible: true, Deleted: tc.Mode == "parent-invalid"}}, nil
			}, Commit: func(ctx context.Context, r CommitRequest) (memoryservice.ProposalResult, error) {
				out := obj("scene", r.SceneID, "candidate", r.Candidate, "priority", scheduler.EffectiveModelPriority(ctx, 0))
				ev := []any{}
				for _, m := range r.Evidence {
					ev = append(ev, m)
				}
				_ = out.Set("evidence", ev)
				for _, f := range []struct {
					k string
					v any
				}{{"parents", r.Parents}, {"entities", r.Entities}, {"commitments", r.Commitments}, {"knowledge", r.KnowledgeEntities}, {"edges", r.KnowledgeEdges}} {
					v, e := SnapshotJSON(f.v)
					if e != nil {
						t.Fatal(e)
					}
					_ = out.Set(f.k, v)
				}
				writes = append(writes, out)
				if tc.Mode == "own-write" {
					valid.Store(false)
				}
				return memoryservice.ProposalResult{Result: assertionproposal.Result{Status: "saved", Memory: obj("id", "saved")}}, nil
			}})
			if e != nil {
				t.Fatal(e)
			}
			defer p.CloseAndWait(context.Background())
			in := Submission{SceneID: "s", Simulation: tc.Mode == "simulation", SourceSnapshots: []*Object{message}, Input: parseTest(t, `{"sourceMessageIds":["a"],"kind":"fact","content":"Synthetic durable fact"}`)}
			if strings.HasPrefix(tc.Mode, "preview-") {
				in.SourceSnapshots = nil
			}
			if tc.Mode == "missing-source" {
				in.SourceSnapshots = []*Object{}
			}
			if tc.Mode == "preview-ref-registered-error" {
				_ = message.Set("sourceEventRef", obj("eventId", "event", "revision", float64(1), "deletionEpoch", float64(0)))
			}
			if tc.Mode == "registration-limited" {
				_ = message.Set("sourceEventRegistrationLimited", true)
			}
			if tc.Mode == "immutable" || tc.Mode == "parent-invalid" {
				in.Memories = []MemorySnapshot{{ID: "parent", Version: 1}}
			}
			if strings.HasPrefix(tc.Mode, "entity-") {
				in.Entities = []EntitySnapshot{{EntityID: "entity", BackingRef: ParentReference{ID: "parent", Version: 1}}}
			}
			if tc.Mode == "entity-invalid" {
				in.EntityValid = func(context.Context, *recovery.WriteSession) (bool, error) { return false, nil }
			}
			if tc.Mode == "read-invalid" || tc.Mode == "own-write" {
				in.Read = &ReadGuard{Identity: "read", Valid: func(context.Context, *recovery.WriteSession) (bool, error) {
					return tc.Mode != "read-invalid" && valid.Load(), nil
				}}
			}
			if tc.Mode == "commitment-freeform" {
				in.CommitmentValid = func(context.Context, *recovery.WriteSession) (bool, error) { return true, nil }
			}
			if tc.Mode == "knowledge-freeform" {
				in.KnowledgeValid = func(context.Context, *recovery.WriteSession) (bool, error) { return true, nil }
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.Mode == "cancelled" {
				cancel()
			}
			result, e := p.Submit(ctx, in)
			if e != nil || result != tc.Result {
				t.Fatal(result, tc.Result, e)
			}
			if tc.Key != nil {
				p.Queue.mu.Lock()
				got := p.Queue.pending[0].job.Key
				p.Queue.mu.Unlock()
				if got != *tc.Key {
					t.Fatal("candidate/provenance hash mismatch", got, *tc.Key)
				}
			}
			if tc.Mode == "immutable" {
				_ = in.Input.Set("content", "MUTATED")
				in.Memories[0].Version = 999
				parts := arr(get(message, "parts"))
				_ = parts[0].(*Object).Set("text", "MUTATED")
			}
			if tc.Mode == "source-changed" {
				valid.Store(false)
			}
			busy.Store(false)
			p.Resume()
			waitIdle(t, p.Queue)
			if got := p.Stats(); got != tc.Stats {
				t.Fatal(got, tc.Stats)
			}
			if len(writes) != len(tc.Writes) {
				t.Fatal(writes, tc.Writes)
			}
			for i, raw := range tc.Writes {
				expected, e := jsjson.ParseBytes(raw)
				if e != nil {
					t.Fatal(e)
				}
				expected.(*Object).Delete("budget")
				a, e := jsjson.Marshal(writes[i])
				if e != nil {
					t.Fatal(e)
				}
				b, e := jsjson.Marshal(expected)
				if e != nil {
					t.Fatal(e)
				}
				var x, y any
				_ = json.Unmarshal(a, &x)
				_ = json.Unmarshal(b, &y)
				if !reflect.DeepEqual(x, y) {
					t.Fatalf("write mismatch\n%s\n%s", a, b)
				}
			}
		})
	}
}
