package memeengine

import (
	"context"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestOutgoingGateUsageAgainstActualSourceFixture(t *testing.T) {
	raw, e := os.ReadFile("../httpapi/testdata/meme-outgoing.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Cases []struct {
			Name            string
			Priority        int
			Action          string
			Before          int
			Original, Calls []struct {
				AssetID string
				Context struct {
					SceneID    string
					Simulation bool
				}
			}
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	for _, row := range fixture.Cases {
		t.Run(row.Name, func(t *testing.T) {
			f := newOutgoingFixture(t)
			ctx := context.Background()
			dates, e := jsdate.New("UTC")
			if e != nil {
				t.Fatal(e)
			}
			assertions, e := assertions.New(f.main, f.authority, assertions.Options{DateParser: dates})
			if e != nil {
				t.Fatal(e)
			}
			memes, e := memeservice.New(ctx, f.main, f.authority, assertions, assertionauthority.Options{DateParser: dates, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }}, t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			s := &Service{main: f.main, authority: f.authority, Memes: memes}
			id := strings.Repeat("a", 64)
			if _, e = f.main.Exec(`INSERT INTO scenes VALUES('s','{"id":"s","adapterId":"a","accountId":"c","learningEnabled":true}'); INSERT INTO scenes VALUES('other','{"id":"other","adapterId":"a","accountId":"c","learningEnabled":true}')`); e != nil {
				t.Fatal(e)
			}
			asset := `{"id":"` + id + `","contentHash":"` + id + `","format":"png","bytes":4,"available":true,"usageCount":0,"sources":[{"sceneId":"s","messageId":"m","senderId":"u"}],"tags":{"appearance":[],"style":[],"subject":[],"emotion":[],"intent":[],"tone":[],"intensity":[],"memeMeaning":[],"ocr":""}}`
			if _, e = f.main.Exec("INSERT INTO meme_assets VALUES(?,?)", id, asset); e != nil {
				t.Fatal(e)
			}

			kind, partID := "media", id
			switch row.Name {
			case "uppercase":
				partID = strings.ToUpper(id)
			case "short":
				partID = "abc"
			case "non-media":
				kind = "text"
			}
			rawPart, _ := json.Marshal(map[string]string{"type": kind, "mediaType": "image", "assetId": partID})
			parts := []json.RawMessage{rawPart}
			if row.Name == "duplicate" {
				parts = append(parts, rawPart)
			}
			gotIDs, wantIDs := []string{}, []string{}
			for _, part := range parts {
				got, e := managedPart(part)
				if e != nil {
					t.Fatal(e)
				}
				if got != "" {
					gotIDs = append(gotIDs, got)
				}
			}
			for _, call := range row.Original {
				if call.Context.SceneID != "s" || call.Context.Simulation {
					t.Fatal("source context changed")
				}
				wantIDs = append(wantIDs, call.AssetID)
			}
			if !reflect.DeepEqual(gotIDs, wantIDs) {
				t.Fatalf("native managed filter %v != actual source %v", gotIDs, wantIDs)
			}
			hook := s.outgoingHook()
			if hook.Priority != float64(row.Priority) || row.Action != "continue" || row.Before != 0 {
				t.Fatal("source hook/event contract changed")
			}
			settlements := 0
			hook.Run(ctx, []dispatch.Segment{{Parts: parts}}, &botruntime.TurnContext{Scene: botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "s"}}, Mode: "real"}, func(result botruntime.HookResult, e error) {
				settlements++
				if e != nil || result.Cancel {
					t.Errorf("source continue != native: %v", e)
				}
			})
			if settlements != 1 {
				t.Fatal("hook settlement")
			}
			if row.Name == "usage-throws" {
				if _, e = f.main.Exec("UPDATE scenes SET value=json_set(value,'$.learningEnabled',json('false')) WHERE id='s'"); e != nil {
					t.Fatal(e)
				}
			}
			// The runtime source-oracle companion separately proves that execution-time
			// recall supplies an empty payload. This test consumes that exact boundary.
			if row.Name == "recalled" {
				parts = nil
			}
			s.recordOutgoing(botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "s"}}, "turn", dispatch.Segment{Parts: parts}, dispatch.Receipt{Status: dispatch.Sent})
			want := len(row.Calls)
			if row.Name == "usage-throws" {
				want = 0
			}
			var got int
			if e = f.main.QueryRow("SELECT json_extract(value,'$.usageCount') FROM meme_assets WHERE id=?", id).Scan(&got); e != nil {
				t.Fatal(e)
			}
			if got != want {
				t.Fatalf("actual SQLite usage %d vs source successful calls %d", got, want)
			}
		})
	}
}
