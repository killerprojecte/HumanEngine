package externaladapter

import (
	"testing"
	"time"
)

func TestHostDerivesPortableIdentityAndOwnsOrigin(t *testing.T) {
	raw := []byte(`{
		"type":"message", "peer":{"kind":"group","id":"peer:a","adapterId":"snowluma"},
		"message":"8", "sender":"bot", "replyTo":"7", "sentAt":"2026-11-25T06:03:45Z", "text":"ordinary text",
		"sceneId":"sim:foreign", "id":"ob:spoof", "origin":"history", "receivedAt":"1900-01-01T00:00:00Z"
	}`)
	ev, err := projectEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	s := scope{adapter: "stationery", account: "owner:two"}
	scene, err := s.scene(ev.Peer)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 25, 6, 3, 46, 0, time.UTC)
	m, err := s.message(scene, ev, now)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"sceneId": scene.ID, "id": messageID(scene, "8"), "senderId": s.sender("bot"),
		"origin": "live", "receivedAt": "2026-11-25T06:03:46.000Z", "replyToId": messageID(scene, "7"),
	} {
		got, _ := m.Get(key)
		if got != want {
			t.Fatalf("%s: got %v, want %v", key, got, want)
		}
	}
	if scene.AdapterID != "stationery" || scene.AccountID != "owner:two" || s.sender("bot") == "bot" {
		t.Fatal("guest rewrote installed identity:", scene)
	}
	if _, err := s.scene(peer{Kind: "simulation", ID: "peer:a"}); err == nil {
		t.Fatal("guest created a simulation")
	}
	if id, err := platformMessage(scene, messageID(scene, "reply:!")); err != nil || id != "reply:!" {
		t.Fatal(id, err)
	}
	other, _ := s.scene(peer{Kind: "group", ID: "other"})
	if _, err := platformMessage(other, messageID(scene, "8")); err == nil {
		t.Fatal("accepted a foreign scene reply")
	}
	foreign, _ := (scope{adapter: "stationery", account: "owner:three"}).scene(ev.Peer)
	if _, err := platformMessage(foreign, messageID(scene, "8")); err == nil {
		t.Fatal("accepted a foreign account reply")
	}
}

func TestPortableIdentityDoesNotCollapseInvalidUnicode(t *testing.T) {
	raw := []byte(`{"type":"recall","peer":{"kind":"group","id":"新聊天室"},"message":"\ud800"}`)
	if _, err := projectEvent(raw); err == nil {
		t.Fatal("accepted a surrogate in a platform identity")
	}
	raw = []byte(`{"type":"recall","peer":{"kind":"group","id":"新聊天室"},"message":"源消息"}`)
	ev, err := projectEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	scene, err := (scope{adapter: "adapter", account: "account"}).scene(ev.Peer)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := platformMessage(scene, messageID(scene, ev.Message)); err != nil || got != "源消息" {
		t.Fatal(got, err)
	}
}
