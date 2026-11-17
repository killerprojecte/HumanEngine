package memeengine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

func memeCompletion(name string, args *Object) botruntime.Completion {
	call := replyplan.ToolCall{ID: name, Name: name, Arguments: args}
	return botruntime.Completion{Completion: replyplan.Completion{ToolCalls: []replyplan.ToolCall{call}, Assistant: fields("role", "assistant", "content", "", "toolCalls", []any{fields("id", name, "name", name, "arguments", args)})}}
}
func memeRequest(req botruntime.ModelRequest) *Object {
	for _, message := range req.Messages {
		if get(message, "role") == "user" {
			value, err := jsjson.Parse(str(get(message, "content")))
			if err != nil {
				panic(err)
			}
			return obj(value)
		}
	}
	return fields()
}

// The choices are deterministic model fixtures. Search, scoped data reads,
// Planner/Replyer orchestration, media resolution and adapter ACK are real owners.
func TestNaturalMemePlanningLoopNativeOwners(t *testing.T) {
	for _, choice := range []string{"media", "textUnselectedGone", "noReply", "seriousText", "emptyText", "placeholderRepair", "revokedSelected", "recalledSelected", "permissionRevoked", "hookReplaced"} {
		t.Run(choice, func(t *testing.T) {
			f := newOutgoingFixture(t)
			ctx := context.Background()
			dates, err := jsdate.New("UTC")
			if err != nil {
				t.Fatal(err)
			}
			store, err := assertions.New(f.main, f.authority, assertions.Options{DateParser: dates})
			if err != nil {
				t.Fatal(err)
			}
			mediaRoot := t.TempDir()
			memes, err := memeservice.New(ctx, f.main, f.authority, store, assertionauthority.Options{DateParser: dates, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }}, mediaRoot)
			if err != nil {
				t.Fatal(err)
			}
			authority, err := sourceevents.NewRuntimeAuthority(f.events)
			if err != nil {
				t.Fatal(err)
			}
			read, err := modelread.NewRuntime(store, f.events, f.main, f.authority)
			if err != nil {
				t.Fatal(err)
			}
			scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "s", Kind: "group", AdapterID: "fixture", AccountID: "bot", PlatformSceneID: "group"}, ParticipationEnabled: true, LearningEnabled: false, ProactiveEnabled: true}
			sceneJSON := `{"id":"s","name":"fixture","kind":"group","adapterId":"fixture","accountId":"bot","platformSceneId":"group","participationEnabled":true,"learningEnabled":false,"proactiveEnabled":true}`
			if _, err = f.main.Exec("INSERT INTO scenes VALUES('s',?)", sceneJSON); err != nil {
				t.Fatal(err)
			}
			assetIDs := []string{}
			assetBytes := map[string][]byte{}
			for i := 0; i < 2; i++ {
				img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
				img.SetNRGBA(0, 0, color.NRGBA{R: uint8(120 + i), G: 80, A: 255})
				var buffer bytes.Buffer
				if err = png.Encode(&buffer, img); err != nil {
					t.Fatal(err)
				}
				content := buffer.Bytes()
				sum := sha256.Sum256(content)
				id := hex.EncodeToString(sum[:])
				assetIDs = append(assetIDs, id)
				assetBytes[id] = content
				tags := fields("appearance", []any{"colored square"}, "style", []any{"cartoon"}, "subject", []any{}, "emotion", []any{"happy"}, "intent", []any{"celebrate"}, "tone", []any{"playful"}, "intensity", []any{"low"}, "memeMeaning", []any{"congratulations"}, "ocr", "")
				asset := fields("id", id, "contentHash", id, "format", "png", "bytes", float64(len(content)), "available", true, "analyzed", true, "usageCount", float64(0), "sources", []any{fields("sceneId", "s", "messageId", fmt.Sprintf("asset-source-%d", i), "senderId", "u")}, "tags", tags)
				raw, _ := jsjson.Stringify(asset)
				if _, err = f.main.Exec("INSERT INTO meme_assets VALUES(?,?)", id, raw); err != nil {
					t.Fatal(err)
				}
				if _, err = f.main.Exec("INSERT INTO meme_sources VALUES(?,?,?,?)", id, "s", fmt.Sprintf("asset-source-%d", i), "u"); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(mediaRoot, id+".png"), content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			policy, err := replyplan.New(func(a, b string) (int, error) { return strings.Compare(a, b), nil }, dates)
			if err != nil {
				t.Fatal(err)
			}
			var runtime *botruntime.Runtime
			var service *Service
			searches, replyerCalls := 0, 0
			sent := []dispatch.Segment{}
			selected := ""
			finalAsset := ""
			receiptCandidates := []string{}
			mediaChoice := choice == "media" || choice == "revokedSelected" || choice == "recalledSelected" || choice == "hookReplaced"
			model := botruntime.ModelFunc(func(_ context.Context, req botruntime.ModelRequest, done func(botruntime.Completion, error)) {
				input := memeRequest(req)
				if req.Stage == "decision" {
					done(memeCompletion("participation.decide", fields("action", "reply", "reasonCode", "fixture", "targetMessageIds", []any{"hello"})), nil)
					return
				}
				if req.Stage == "plan" {
					policyText := ""
					for _, m := range req.Messages {
						if get(m, "role") == "system" {
							policyText += str(get(m, "content"))
						}
					}
					if !strings.Contains(policyText, "proactively use memes.search even if the user did not explicitly request an image") || !strings.Contains(policyText, "independent meaningful text, or noReply") {
						t.Error("actual Planner request lost natural meme policy")
					}
					advertised := false
					for _, tool := range req.Tools {
						if get(tool, "name") == "memes.search" {
							advertised = strings.Contains(str(get(tool, "description")), "real asset id")
						}
					}
					if !advertised {
						t.Error("actual Planner missing semantic search tool")
					}

					receipts := []*Object{}
					for _, message := range req.Messages {
						if get(message, "role") == "tool" {
							v, e := jsjson.Parse(str(get(message, "content")))
							if e != nil {
								t.Error(e)
							}
							receipts = append(receipts, obj(v))
						}
					}
					if choice != "seriousText" {
						if len(receipts) == 0 {
							searches++
							done(memeCompletion("memes.search", fields("query", "unmatched-first-query")), nil)
							return
						}
						if len(arr(get(obj(get(receipts[0], "value")), "items"))) != 0 {
							t.Error("first query should be empty")
						}
						if len(receipts) == 1 {
							searches++
							query := "celebrate"
							if choice == "noReply" || choice == "emptyText" || choice == "placeholderRepair" {
								query = "unmatched-second-query"
							}
							done(memeCompletion("memes.search", fields("query", query)), nil)
							return
						}
					}
					assets := []any{}
					if len(receipts) > 1 {
						items := arr(get(obj(get(receipts[1], "value")), "items"))
						for _, item := range items {
							candidate := obj(item)
							receiptCandidates = append(receiptCandidates, str(get(candidate, "id")))
							tags := obj(get(candidate, "tags"))
							if !reflect.DeepEqual(get(tags, "memeMeaning"), []any{"congratulations"}) {
								t.Error("semantic tags lost", tags)
							}
						}
						if mediaChoice {
							if len(items) != 2 {
								t.Errorf("real search candidates=%d", len(items))
								done(botruntime.Completion{}, fmt.Errorf("missing candidates"))
								return
							}
							selected = str(get(obj(items[0]), "id"))
							for _, item := range items {
								assets = append(assets, fields("assetId", get(obj(item), "id"), "evidenceId", get(receipts[1], "id")))
							}
						}
						if choice == "textUnselectedGone" {
							if _, e := f.main.Exec("UPDATE meme_assets SET value=json_set(value,'$.disabled',json('true'))"); e != nil {
								t.Error(e)
							}
						}
					}
					contract := obj(get(input, "planContract"))
					action := "reply"
					if choice == "noReply" {
						action = "noReply"
					}
					done(memeCompletion("reply.plan", fields("action", action, "deliveryTool", "messages.send", "targetMessageIds", get(contract, "targetMessageIds"), "contextMessageIds", get(contract, "contextMessageIds"), "strategy", "Respond naturally with only the chosen expression.", "constraints", []any{}, "facts", []any{}, "assets", assets, "emoji", []any{}, "memorySelections", []any{}, "expressionGoals", []any{}, "actions", []any{})), nil)
					return
				}
				if req.Stage == "replyer" {
					replyerCalls++
					policyText := ""
					for _, m := range req.Messages {
						if get(m, "role") == "system" {
							policyText += str(get(m, "content"))
						}
					}
					if !strings.Contains(policyText, "a media-only reply is valid") {
						t.Error("actual Replyer request lost media-only policy")
					}
					for _, tool := range req.Tools {
						if get(tool, "name") == "memes.search" {
							t.Error("search leaked into Replyer")
						}
					}
					parts := []any{fields("type", "text", "text", "太好了，终于完成了！")}
					if mediaChoice {
						if get(obj(arr(get(obj(get(input, "plan")), "assets"))[0]), "assetId") != selected {
							t.Error("selected asset lost at handoff")
						}
						parts = []any{fields("type", "media", "mediaType", "image", "assetId", selected)}
					}
					if choice == "seriousText" {
						parts = []any{fields("type", "text", "text", "先把情况说清楚，我们一起处理。")}
					}
					if choice == "placeholderRepair" && replyerCalls == 1 {
						parts = []any{fields("type", "text", "text", "[表情包：庆祝]")}
					}
					done(memeCompletion("messages.send", fields("operationId", fmt.Sprintf("expression-%d", replyerCalls), "segments", []any{fields("replyToId", "hello", "parts", parts)})), nil)
					return
				}
				done(botruntime.Completion{}, fmt.Errorf("unexpected stage %s", req.Stage))
			})
			cfg := botruntime.DefaultSettings()
			cfg.DebounceMS = 0
			cfg.GroupCooldownMS = 0
			cfg.Persona = "Fixture"
			cfg.Timezone = "UTC"
			runtime, err = botruntime.New(cfg, botruntime.Services{Models: model, Authority: authority, Policy: policy, Dates: dates, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }})
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			service = &Service{main: f.main, authority: f.authority, Memes: memes, Sources: f.events, Runtime: runtime, TurnRead: read, Read: read.Policy}
			service.installOutgoing()
			runtime.AddExtension(service.ComposeExtension(botruntime.Extension{}))
			runtime.AddHook(botruntime.Hook{ID: "fixture-change", Priority: 0, Run: func(_ context.Context, segments []dispatch.Segment, c *botruntime.TurnContext, done func(botruntime.HookResult, error)) {
				var e error
				current := ""
				if len(segments) > 0 && len(segments[0].Parts) > 0 {
					current, _ = managedPart(segments[0].Parts[0])
				}
				finalAsset = current
				switch choice {
				case "revokedSelected":
					_, e = f.main.Exec("UPDATE meme_assets SET value=json_set(value,'$.disabled',json('true')) WHERE id=?", current)
				case "recalledSelected":
					for i, id := range assetIDs {
						if id == current {
							_, e = f.main.Exec("INSERT INTO recall_tombstones VALUES('s',?,'2026-11-17T00:00:00Z')", fmt.Sprintf("asset-source-%d", i))
						}
					}
				case "permissionRevoked":
					_, e = f.main.Exec("UPDATE scenes SET value=json_set(value,'$.memoryReadEnabled',json('false'),'$.memoryReadRevision',1) WHERE id='s'")
				case "hookReplaced":
					other := assetIDs[0]
					if other == current {
						other = assetIDs[1]
					}
					_, e = f.main.Exec("UPDATE meme_assets SET value=json_set(value,'$.disabled',json('true')) WHERE id=?", current)
					finalAsset = other
					raw, _ := json.Marshal(map[string]any{"type": "media", "mediaType": "image", "assetId": other})
					segments[0].Parts = []json.RawMessage{raw}
				}
				done(botruntime.HookResult{Segments: segments}, e)
			}})
			runtime.AddAdapter("fixture", &botruntime.Adapter{Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, segment dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
				// Adapter resolves the exact outgoing bytes, after asynchronous preparation
				// and the Runtime's final authorization callback, just like a real transport.
				if e := before(); e != nil {
					return dispatch.Rejected[dispatch.Receipt](e)
				}
				for _, part := range segment.Parts {
					id, e := managedPart(part)
					if e != nil {
						return dispatch.Rejected[dispatch.Receipt](e)
					}
					if id != "" {
						original, e := memes.Original(ctx, memeservice.Context{SceneID: "s"}, id)
						if e != nil {
							return dispatch.Rejected[dispatch.Receipt](e)
						}
						content, e := os.ReadFile(original.Path)
						if e != nil || !bytes.Equal(content, assetBytes[id]) {
							t.Error("wrong physical media", e)
						}
					}
				}
				sent = append(sent, segment)
				id := "delivered"
				return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Sent, MessageID: &id})
			}}})
			if err = runtime.RegisterScene(scene); err != nil {
				t.Fatal(err)
			}
			at := time.Now().UTC().Format(time.RFC3339Nano)
			text := "终于搞定了，哈哈！"
			if choice == "seriousText" {
				text = "我今天遇到很严重的问题，需要认真讨论。"
			}
			topic, e := runtime.Ingest(ctx, "s", fields("id", "hello", "sceneId", "s", "senderId", "person", "origin", "live", "status", "available", "sentAt", at, "receivedAt", at, "parts", []any{fields("type", "text", "text", text)}), false)
			if e != nil {
				t.Fatal(e)
			}
			handle, e := runtime.Run("s", topic, false)
			if e != nil {
				t.Fatal(e)
			}
			select {
			case <-handle.Done:
			case <-time.After(10 * time.Second):
				t.Fatal("native meme turn blocked")
			}
			result := handle.Wait()
			wantSearch := 2
			if choice == "seriousText" {
				wantSearch = 0
			}
			if searches != wantSearch {
				t.Errorf("searches %d != %d; result=%+v", searches, wantSearch, result)
			}
			wantSent := 1
			if choice == "noReply" || choice == "revokedSelected" || choice == "recalledSelected" || choice == "permissionRevoked" {
				wantSent = 0
			}
			if len(sent) != wantSent {
				t.Errorf("sent %d != %d; result=%+v", len(sent), wantSent, result)
			}
			if choice == "placeholderRepair" && replyerCalls != 2 {
				t.Errorf("placeholder was not repaired: %d; %+v", replyerCalls, result)
			}
			if choice == "noReply" && replyerCalls != 0 {
				t.Error("noReply generated a reply")
			}
			if len(sent) == 1 && mediaChoice {
				var part map[string]any
				_ = json.Unmarshal(sent[0].Parts[0], &part)
				if part["type"] != "media" || part["assetId"] != finalAsset || len(sent[0].Parts) != 1 {
					t.Error("not exact media-only output", part)
				}
			}
		})
	}
}
