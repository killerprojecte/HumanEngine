package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeengine"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"github.com/killerprojecte/HumanEngine/backend/internal/scenestore"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	_ "github.com/mattn/go-sqlite3"
)

func rulesCompletion(name string, args any) botruntime.Completion {
	call := replyplan.ToolCall{ID: name, Name: name, Arguments: args}
	return botruntime.Completion{Completion: replyplan.Completion{ToolCalls: []replyplan.ToolCall{call}, Assistant: memoryToolObject("role", "assistant", "content", "", "toolCalls", []any{memoryToolObject("id", name, "name", name, "arguments", args)})}}
}
func rulesData(request botruntime.ModelRequest) *jsjson.Object {
	out := new(jsjson.Object)
	for _, m := range request.Messages {
		if rulesString(m, "role") == "assistant" || rulesString(m, "role") == "tool" {
			break
		}
		if rulesString(m, "role") == "user" {
			v, e := jsjson.Parse(rulesString(m, "content"))
			if e == nil {
				for _, field := range v.(*jsjson.Object).Members() {
					_ = out.Set(field.Key, field.Value)
				}
			}
		}
	}
	return out
}
func rulesNoReply(data *jsjson.Object) botruntime.Completion {
	raw, _ := data.Get("planContract")
	contract := raw.(*jsjson.Object)
	target, _ := contract.Get("targetMessageIds")
	messages, _ := contract.Get("contextMessageIds")
	return rulesCompletion("reply.plan", memoryToolObject("action", "noReply", "deliveryTool", "messages.send", "targetMessageIds", target, "contextMessageIds", messages, "strategy", "No reply needed.", "constraints", []any{}, "facts", []any{}, "assets", []any{}, "emoji", []any{}, "actions", []any{}))
}

// This is the production rules extension on a real Runtime/source/recovery/habit
// graph with a deterministic model. No provider, native UI or mocked authority.
func TestRulesOnDemandRealRuntime(t *testing.T) {
	for _, scenario := range []string{"unread", "narrow", "recall-delivered", "privacy-aba", "privacy-off", "read-off"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			root := t.TempDir()
			live := filepath.Join(root, "live")
			if err := os.Mkdir(live, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(live, "main.sqlite")
			db, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec("PRAGMA journal_mode=WAL"); err != nil {
				t.Fatal(err)
			}
			authority, err := recovery.OpenInstallationGate(ctx, db, path, filepath.Join(root, "authority"))
			if err != nil {
				t.Fatal(err)
			}
			defer authority.Close()
			// One connection catches a nested root publication during recall validation.
			db.SetMaxOpenConns(1)
			_, err = db.Exec(`CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT NOT NULL);CREATE TABLE config(key TEXT PRIMARY KEY,value TEXT NOT NULL);CREATE TABLE recall_tombstones(scene_id TEXT NOT NULL,message_id TEXT NOT NULL,recalled_at TEXT NOT NULL,PRIMARY KEY(scene_id,message_id))`)
			if err != nil {
				t.Fatal(err)
			}
			sceneRecord := memoryToolObject("id", "s", "name", "s", "kind", "group", "adapterId", "fixture", "accountId", "bot", "platformSceneId", "s", "participationEnabled", true, "learningEnabled", false, "proactiveEnabled", true, "memoryReadEnabled", true, "personalizationEnabled", true, "memoryReadRevision", float64(0))
			if scenario == "privacy-off" {
				_ = sceneRecord.Set("personalizationEnabled", false)
			}
			if scenario == "read-off" {
				_ = sceneRecord.Set("memoryReadEnabled", false)
			}
			sceneStore := scenestore.Store{Main: db, Authority: authority}
			if _, err = sceneStore.Save(ctx, sceneRecord); err != nil {
				t.Fatal(err)
			}
			manual := `[{"id":"fixed","scope":"global","enabled":true,"nonOverridable":true,"content":"MANUAL_CANARY"}]`
			if _, err = db.Exec("INSERT INTO config VALUES('rules',?)", manual); err != nil {
				t.Fatal(err)
			}
			sources, err := sourceevents.New(db, authority, sourceevents.Options{})
			if err != nil {
				t.Fatal(err)
			}
			assertionStore, err := assertions.New(db, authority, assertions.Options{})
			if err != nil {
				t.Fatal(err)
			}
			var habitReads atomic.Int64
			now := float64(time.Now().UnixMilli())
			habits, err := habitservice.New(ctx, db, authority, habitservice.Options{Now: func(*habitservice.Service) float64 { habitReads.Add(1); return now }})
			if err != nil {
				t.Fatal(err)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 600; i++ {
				id := fmt.Sprintf("hint-%d", i)
				record := memoryToolObject("id", id, "sceneId", "s", "kind", "sceneRule", "content", fmt.Sprintf("HINT_CANARY_%03d %s", i, strings.Repeat("context", 100)), "evidenceVersion", float64(2), "sources", []any{memoryToolObject("sceneId", "s", "messageId", id, "senderId", "speaker", "direction", float64(1), "timestamp", now)}, "weight", float64(0), "confidence", float64(0), "status", "active", "locked", false, "learnedAt", time.Now().UTC().Format(time.RFC3339Nano))
				if i == 599 {
					_ = record.Set("context", "technical")
				}
				raw, e := jsjson.Stringify(record)
				if e != nil {
					t.Fatal(e)
				}
				if _, err = tx.Exec("INSERT INTO habits VALUES(?,?,?)", id, id, raw); err != nil {
					t.Fatal(err)
				}
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			read, err := modelread.NewRuntime(assertionStore, sources, db, authority)
			if err != nil {
				t.Fatal(err)
			}
			sourceAuthority, err := sourceevents.NewRuntimeAuthority(sources)
			if err != nil {
				t.Fatal(err)
			}
			sourceAuthority = sourceAuthority.AppendRecallPreparation(habits.RuntimeRecallPreparation())
			dates, err := jsdate.New("UTC")
			if err != nil {
				t.Fatal(err)
			}
			compare := func(a, b string) (int, error) { return strings.Compare(a, b), nil }
			policy, err := replyplan.New(compare, dates)
			if err != nil {
				t.Fatal(err)
			}
			settings := botruntime.DefaultSettings()
			settings.TopicAdmissionEnabled = false
			settings.PromptContextWindowEnabled = false
			settings.Hour = func(float64, string) (int, error) { return 12, nil }
			var runtime *botruntime.Runtime
			var requests, initialBytes, legacyBytes int
			var initialMessages []*jsjson.Object
			var finalRules bool
			models := botruntime.ModelFunc(func(call context.Context, request botruntime.ModelRequest, done func(botruntime.Completion, error)) {
				data := rulesData(request)
				hasDecision := false
				for _, tool := range request.Tools {
					if rulesString(tool, "name") == "participation.decide" {
						hasDecision = true
					}
				}
				if hasDecision {
					done(rulesCompletion("participation.decide", memoryToolObject("action", "reply", "reasonCode", "fixture", "targetMessageIds", []any{"question"})), nil)
					return
				}
				requests++
				encoded, e := jsjson.Stringify(memoryToolArray(request.Messages))
				if e != nil {
					done(botruntime.Completion{}, e)
					return
				}
				if requests == 1 {
					initialBytes = len(encoded)
					for _, m := range request.Messages {
						initialMessages = append(initialMessages, m.Clone())
					}
					if habitReads.Load() != 0 || strings.Contains(encoded, "HINT_CANARY") {
						t.Error("learned hints read or serialized during initial preparation")
					}
					if !strings.Contains(encoded, "Read rules.list only when learned scene or expression preferences are relevant") {
						t.Error("Planner was not told when to read learned rules")
					}
					if !strings.Contains(encoded, "MANUAL_CANARY") {
						t.Error("manual rule missing")
					}
					if scenario == "unread" {
						done(rulesNoReply(data), nil)
						return
					}
					done(rulesCompletion("rules.list", memoryToolObject()), nil)
					return
				}
				receipts := []*jsjson.Object{}
				for _, m := range request.Messages {
					if rulesString(m, "role") == "tool" {
						v, e := jsjson.Parse(rulesString(m, "content"))
						if e != nil {
							done(botruntime.Completion{}, e)
							return
						}
						receipts = append(receipts, v.(*jsjson.Object))
					}
				}
				if scenario == "privacy-off" || scenario == "read-off" {
					if habitReads.Load() != 0 || strings.Contains(encoded, "HINT_CANARY") || !strings.Contains(encoded, "MANUAL_CANARY") {
						t.Error("disabled learned access read data or suppressed manual rules")
					}
					done(rulesNoReply(data), nil)
					return
				}
				if requests == 2 {
					if strings.Contains(encoded, "HINT_CANARY") || !strings.Contains(encoded, "limited") {
						t.Error("oversized rules not withheld whole")
					}
					// This source was among the withheld result and must not abort the turn.
					if e = runtime.Recall(ctx, "s", "hint-0"); e != nil {
						done(botruntime.Completion{}, e)
						return
					}
					done(rulesCompletion("rules.list", memoryToolObject("query", "HINT_CANARY_599", "context", "general")), nil)
					return
				}
				latest, _ := receipts[len(receipts)-1].Get("value")
				value := latest.(*jsjson.Object)
				raw, _ := value.Get("learned")
				rows, _ := raw.([]any)
				if requests == 3 {
					if len(rows) != 0 {
						t.Error("technical hint leaked into general query")
					}
					done(rulesCompletion("rules.list", memoryToolObject("query", "HINT_CANARY_599", "context", "technical")), nil)
					return
				}
				if len(rows) != 1 || !strings.Contains(encoded, "HINT_CANARY_599") {
					t.Error("narrow query did not expose exact hint")
				}
				finalRules = true
				if scenario == "recall-delivered" {
					e = runtime.Recall(ctx, "s", "hint-599")
				}
				if scenario == "privacy-aba" {
					disabled := sceneRecord.Clone()
					_ = disabled.Set("personalizationEnabled", false)
					_, e = sceneStore.Save(call, disabled)
					if e == nil {
						_, e = sceneStore.Save(call, sceneRecord)
					}
				}
				if e != nil {
					done(botruntime.Completion{}, e)
					return
				}
				done(rulesNoReply(data), nil)
			})
			runtime, err = botruntime.New(settings, botruntime.Services{Models: models, Authority: sourceAuthority, Policy: policy, Dates: dates, Compare: compare})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if e := runtime.CloseAndWait(ctx); e != nil {
					t.Error(e)
				}
			}()
			app := &Application{Engine: &memeengine.Service{Runtime: runtime, TurnRead: read, Sources: sources}}
			owners := Owners{Main: db, Authority: authority, Assertions: assertionStore, Sources: sources, Habits: habits}
			app.composeRules(owners)
			scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "s", Kind: "group", AdapterID: "fixture", AccountID: "bot", PlatformSceneID: "s"}, ParticipationEnabled: true, ProactiveEnabled: true}
			if err = runtime.RegisterScene(scene); err != nil {
				t.Fatal(err)
			}
			at := time.Now().UTC().Format(time.RFC3339Nano)
			message := memoryToolObject("id", "question", "sceneId", "s", "senderId", "user", "origin", "live", "status", "available", "sentAt", at, "receivedAt", at, "parts", []any{memoryToolObject("type", "text", "text", "How should we answer technical questions?")})
			if _, err = runtime.Ingest(ctx, "s", message, false); err != nil {
				t.Fatal(err)
			}
			snapshot, err := runtime.Snapshot("s")
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Topics) == 0 {
				t.Fatal("no topic")
			}
			handle, err := runtime.Run("s", snapshot.Topics[0].ID, false)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-handle.Done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			result := handle.Wait()
			if scenario == "privacy-off" || scenario == "read-off" {
				if requests != 2 || habitReads.Load() != 0 {
					t.Fatal("disabled reads touched learned storage")
				}
			} else if scenario == "unread" {
				if requests != 1 || habitReads.Load() != 0 {
					t.Fatal("unexpected learned reads")
				}
			} else {
				if requests != 4 || !finalRules {
					t.Fatalf("rounds=%d final=%v result=%+v", requests, finalRules, result)
				}
			}
			if scenario == "narrow" || scenario == "unread" || scenario == "privacy-off" || scenario == "read-off" {
				if result.Err != nil {
					t.Fatal(result.Err)
				}
			} else if result.Err == nil && result.Status != "cancelled" {
				t.Fatalf("revoked advice accepted: %+v", result)
			}
			all, err := habits.List(ctx, habitservice.Context{SceneID: "s"})
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range initialMessages {
				if rulesString(m, "role") != "user" {
					continue
				}
				v, e := jsjson.Parse(rulesString(m, "content"))
				if e != nil {
					t.Fatal(e)
				}
				data := v.(*jsjson.Object)
				raw, ok := data.Get("evidence")
				if !ok {
					continue
				}
				evidence := raw.([]any)
				evidence = append(evidence, memoryToolObject("id", "context:0", "value", memoryToolObject("learnedHints", memoryToolArray(all), "learnedHintPolicy", "Soft learned hints apply only in their declared scene and context.")))
				_ = data.Set("evidence", evidence)
				encoded, e := jsjson.Stringify(data)
				if e != nil {
					t.Fatal(e)
				}
				_ = m.Set("content", encoded)
			}
			legacy, e := jsjson.Stringify(memoryToolArray(initialMessages))
			if e != nil {
				t.Fatal(e)
			}
			legacyBytes = len(legacy)
			budget, e := replyplan.NewEvidenceBudget(nil)
			if e != nil {
				t.Fatal(e)
			}
			if e = budget.AssertWithin(initialMessages); e == nil || e.Error() != "planner_evidence_budget_exhausted" {
				t.Fatalf("reconstructed eager payload should exhaust unchanged budget: %v", e)
			}

			if legacyBytes <= 131072 || initialBytes >= 131072 {
				t.Fatalf("measured bytes initial=%d legacy=%d", initialBytes, legacyBytes)
			}
			t.Logf("actual initial Planner bytes=%d; same request plus legacy hints=%d", initialBytes, legacyBytes)
		})
	}
}
