package botruntime

import (
	"context"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"os"
	"strings"
	"testing"
)

// This compares payload/filter results at the source listener boundary. It does
// not claim that goroutine scheduling matches every JavaScript microtask order.
func TestSegmentSentActualSourceEightCases(t *testing.T) {
	raw, e := os.ReadFile("../httpapi/testdata/meme-outgoing.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Cases []struct {
			Name   string
			Before int
			Calls  []struct{ AssetID string }
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	for _, row := range fixture.Cases {
		t.Run(row.Name, func(t *testing.T) {
			r := new(Runtime)
			a := newTestAuthority()
			r.services.Authority = a
			root, _ := r.work.Start()
			id := strings.Repeat("a", 64)
			kind := "media"
			switch row.Name {
			case "uppercase":
				id = strings.ToUpper(id)
			case "short":
				id = "abc"
			case "non-media":
				kind = "text"
			}
			part, _ := json.Marshal(map[string]string{"type": kind, "mediaType": "image", "assetId": id})
			parts := []json.RawMessage{part}
			if row.Name == "duplicate" {
				parts = append(parts, part)
			}
			result := make(chan []string, 1)
			seq := NewSegmentSentSequencer(func(_ Scene, _ string, s dispatch.Segment, _ dispatch.Receipt) {
				ids := []string{}
				for _, raw := range s.Parts {
					var p struct{ Type, AssetID string }
					if e := json.Unmarshal(raw, &p); e != nil {
						t.Error(e)
					}
					valid := p.Type == "media" && len(p.AssetID) == 64
					for _, b := range []byte(p.AssetID) {
						valid = valid && (b >= '0' && b <= '9' || b >= 'a' && b <= 'f')
					}
					if valid {
						ids = append(ids, p.AssetID)
					}
				}
				result <- ids
			})
			receipt := dispatch.Receipt{Status: dispatch.Sent}
			sentID := "sent"
			if row.Name != "no-receipt" {
				receipt.MessageID = &sentID
			}
			reservation := seq.reserve(r, Scene{ID: "s"}, "turn", dispatch.Segment{Parts: parts}, receipt)
			select {
			case <-result:
				t.Fatal("callback preceded ACK publication")
			default:
			}
			if row.Before != 0 {
				t.Fatal("source prepublication changed")
			}
			if row.Name == "recalled" {
				a.mu.Lock()
				a.recalled["s|sent"] = true
				a.mu.Unlock()
			}
			root()
			r.work.Seal()
			reservation.Release()
			ids := <-result
			if len(ids) != len(row.Calls) {
				t.Fatalf("native calls %v vs source %+v", ids, row.Calls)
			}
			for i, v := range ids {
				if v != row.Calls[i].AssetID {
					t.Fatal(v, row.Calls[i])
				}
			}
			if e = r.work.Wait(context.Background()); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestSharedSegmentSequencerUsesReservationRuntime(t *testing.T) {
	first, second := new(Runtime), new(Runtime)
	a, b := newTestAuthority(), newTestAuthority()
	first.services.Authority = a
	second.services.Authority = b
	a.recalled["same|sent"] = true
	firstRoot, _ := first.work.Start()
	secondRoot, _ := second.work.Start()
	result := make(chan int, 2)
	sequence := NewSegmentSentSequencer(func(_ Scene, _ string, segment dispatch.Segment, _ dispatch.Receipt) { result <- len(segment.Parts) })
	id := "sent"
	segment := dispatch.Segment{Parts: []json.RawMessage{json.RawMessage(`{"type":"text","text":"synthetic"}`)}}
	one := sequence.reserve(first, Scene{ID: "same"}, "first", segment, dispatch.Receipt{MessageID: &id})
	two := sequence.reserve(second, Scene{ID: "same"}, "second", segment, dispatch.Receipt{MessageID: &id})
	firstRoot()
	secondRoot()
	first.work.Seal()
	second.work.Seal()
	one.Release()
	two.Release()
	if got := <-result; got != 0 {
		t.Fatal("first recalled payload escaped", got)
	}
	if got := <-result; got != 1 {
		t.Fatal("second reservation borrowed first authority", got)
	}
	if e := first.work.Wait(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := second.work.Wait(context.Background()); e != nil {
		t.Fatal(e)
	}
}
