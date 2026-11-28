package sourceevents

import (
	"context"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/ingressrouting"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestLateSenderNamePreservesNewerInlineName(t *testing.T) {
	f, _ := runtimeFixture(t, Options{})
	ctx := t.Context()
	reader, err := recovery.NewReadPublisher(ctx, f.main, f.authority)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.CloseAndWait(context.Background())
	owner, err := ingressrouting.NewSQLiteRepository(f.main, f.authority)
	if err != nil {
		t.Fatal(err)
	}
	scene := dispatch.SceneRef{ID: "ob:s:WyJhZGFwdGVyIiwiNDIiLCJncm91cCIsIjg4Il0", AdapterID: "adapter", AccountID: "42", Kind: "group", PlatformSceneID: "88"}
	runtimePut(t, f.main, "scenes", scene.ID, `{"id":"ob:s:WyJhZGFwdGVyIiwiNDIiLCJncm91cCIsIjg4Il0","adapterId":"adapter","accountId":"42","kind":"group","platformSceneId":"88"}`)
	runtimePut(t, f.main, "config", "identity:"+scene.ID+":sender", `{"senderPlatformId":"19"}`)
	found, err := ingressrouting.RefreshSenderName(ctx, f.main, f.authority, reader, scene, "sender", "19", func(c context.Context) (string, error) {
		err := owner.SaveIdentity(c, scene.ID, "sender", runtimeObject(t, `{"senderDisplayName":"Current card","senderPlatformId":"19"}`))
		return "Late profile", err
	})
	if err != nil || !found {
		t.Fatal(found, err)
	}
	row, exists, err := owner.Identity(ctx, scene.ID, "sender")
	if err != nil || !exists || stringify(t, row) != `{"senderDisplayName":"Current card","senderPlatformId":"19"}` {
		t.Fatalf("current card was not preserved: %v %v %v", row, exists, err)
	}
	foreign := scene
	foreign.AccountID = "43"
	found, err = ingressrouting.RefreshSenderName(ctx, f.main, f.authority, reader, foreign, "sender", "19", func(context.Context) (string, error) {
		t.Fatal("contacted a peer for an out-of-scope row")
		return "", nil
	})
	if err != nil || found {
		t.Fatal(found, err)
	}
}
