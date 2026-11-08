package memeservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"net/http"
	"strings"
	"testing"
)

func TestProjectedImportAndAnalysisBindCompleteSourceSnapshot(t *testing.T) {
	for _, kind := range []string{"import", "analysis"} {
		for _, change := range []string{"none", "source-add", "source-edit", "source-revoke", "recall", "authority-gap"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				f, s, codec, c, source, b, asset := nativeImageFixture(t)
				ctx, release, e := f.authority.OperationContext(context.Background(), f.main)
				if e != nil {
					t.Fatal(e)
				}
				defer release()
				identity := sourceevents.Identity{SceneID: "s", AdapterID: "a", AccountID: "b", MessageID: "m", SenderID: "p"}
				if _, e = f.events.Observe(ctx, sourceevents.Change{Kind: "ingress", Identity: identity, BodyHash: strings.Repeat("1", 64)}); e != nil {
					t.Fatal(e)
				}
				value, projection, e := s.CaptureProjection(ctx, "s", func(operation context.Context) (*Object, error) {
					if kind == "import" {
						return s.Intake(operation, c, b, source, false, codec)
					}
					models := modelFixture(t, codec, func(w http.ResponseWriter, r *http.Request) { modelReply(w, emptyTags()) })
					return s.Analyze(operation, c, str(get(asset, "id")), models, codec, AnalyzeOptions{Force: true})
				})
				if e != nil {
					t.Fatal(e)
				}
				if len(arr(get(value, "sources"))) != 0 || projection == nil || len(arr(get(projection.asset, "sources"))) != 1 {
					t.Fatal("capture lost full source identity")
				}
				switch change {
				case "source-add":
					second := fields("sceneId", "s", "messageId", "m2", "senderId", "p")
					evidence := copyObject(second)
					set(evidence, "available", true)
					set(evidence, "isBot", false)
					_, e = s.Intake(ctx, Context{SceneID: "s", SourceEvidence: []*Object{evidence}}, b, second, false, codec)
				case "source-edit":
					_, e = f.events.Observe(ctx, sourceevents.Change{Kind: "edit", Identity: identity, BodyHash: strings.Repeat("2", 64), EditID: "edit-1", Order: sourceevents.Order{Kind: "platform_sequence", Sequence: 1}})
				case "source-revoke":
					e = f.events.RevokeAll(ctx, "s", "m")
				case "recall":
					_, e = s.Recall(ctx, "s", "m")
				case "authority-gap":
					_, e = f.authority.BeginDurable(ctx, f.main, func(*recovery.WriteSession) (recovery.Revocation, error) {
						return recovery.Source{SceneID: "s", MessageID: "m"}, nil
					})
				}
				if e != nil {
					t.Fatal(e)
				}
				if change == "source-edit" || change == "source-revoke" {
					current, e := s.AdminGet(ctx, str(get(asset, "id")))
					if e != nil {
						t.Fatal(e)
					}
					a, _ := jsjson.Stringify(current)
					b, _ := jsjson.Stringify(projection.asset)
					if a != b {
						t.Fatal("source-only fixture changed meme row")
					}
				}
				raw, e := jsjson.Marshal(value)
				if e != nil {
					t.Fatal(e)
				}
				emitted := false
				e = s.PublishProjection(ctx, projection, raw, func([]byte) error { emitted = true; return nil })
				if change == "none" {
					if e != nil || !emitted {
						t.Fatal(e)
					}
				} else if e == nil || emitted {
					t.Fatal("changed source published sanitized response", e)
				}
			})
		}
	}
}
