package memeservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"testing"
)

func TestModelReadNativeTicketRevocationAndIdentity(t *testing.T) {
	f, s := newTestService(t)
	ctx := context.Background()
	policy, e := modelread.New(s.assertions)
	if e != nil {
		t.Fatal(e)
	}
	save := func(raw string) {
		t.Helper()
		if _, e := f.main.Exec("INSERT OR REPLACE INTO scenes(id,value) VALUES('scene',?)", raw); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = policy.Require(ctx, "scene"); e == nil || e.Error() != "memory_read_disabled" {
		t.Fatal(e)
	}
	save(`{"id":"scene"}`)
	ticket, e := policy.Require(ctx, "scene")
	if e != nil || !ticket.PersonalizationEnabled() {
		t.Fatal(e)
	}
	copied := *ticket
	if policy.Valid(ctx, &copied) {
		t.Fatal("copied ticket accepted")
	}
	other, _ := modelread.New(s.assertions)
	if other.Valid(ctx, ticket) {
		t.Fatal("foreign policy accepted ticket")
	}
	save(`{"id":"scene","memoryReadRevision":1}`)
	if policy.Valid(ctx, ticket) {
		t.Fatal("old revision still valid")
	}
	save(`{"id":"scene","memoryReadRevision":1,"personalizationEnabled":false}`)
	ticket, e = policy.Require(ctx, "scene")
	if e != nil || ticket.PersonalizationEnabled() {
		t.Fatal(e)
	}
	if allowed, e := policy.PersonalizationAllowed(ctx, "scene"); e != nil || allowed {
		t.Fatal(allowed, e)
	}
	save(`{"id":"scene","memoryReadRevision":1,"personalizationEnabled":true}`)
	if policy.Valid(ctx, ticket) {
		t.Fatal("personalization transition retained old ticket")
	}
	for _, raw := range []string{`{"id":"scene","memoryReadEnabled":null}`, `{"id":"scene","personalizationEnabled":"yes"}`, `{"id":"scene","memoryReadRevision":-1}`, `{"id":"scene","memoryReadRevision":0.5}`} {
		save(raw)
		if _, e = policy.Require(ctx, "scene"); e == nil || e.Error() != "memory_read_revoked" {
			t.Fatalf("%s: %v", raw, e)
		}
	}
}
