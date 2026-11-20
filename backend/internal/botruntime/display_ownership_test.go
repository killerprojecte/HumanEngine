package botruntime

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

func TestSnapshotAndPreviewNestedDisplayOwnership(t *testing.T) {
	r, authority, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) {}))
	scene := fixtureScene(true)
	ctx := context.Background()
	if _, err := r.IngestScene(ctx, scene, fixtureMessage(scene.ID, "message", "original"), false); err != nil {
		t.Fatal(err)
	}
	part := func(row *Object) *Object { return array(get(row, "parts"))[0].(*Object) }
	snapshot, err := r.Snapshot(scene.ID)
	if err != nil || len(snapshot.Messages) != 1 {
		t.Fatalf("snapshot: %#v, %v", snapshot.Messages, err)
	}
	_ = part(snapshot.Messages[0]).Set("text", "snapshot change")
	preview, err := r.PreviewMessage(scene.ID, "message", authority)
	if err != nil || preview == nil || get(part(preview), "text") != "original" {
		t.Fatalf("snapshot leaked mutation: %v, %v", preview, err)
	}
	_ = part(preview).Set("text", "preview change")
	snapshot, err = r.Snapshot(scene.ID)
	if err != nil || len(snapshot.Messages) != 1 || get(part(snapshot.Messages[0]), "text") != "original" {
		t.Fatalf("preview leaked mutation: %v, %v", snapshot.Messages, err)
	}
	if err := r.Recall(ctx, scene.ID, "message"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = r.Snapshot(scene.ID)
	if err != nil || len(snapshot.Messages) != 1 || get(snapshot.Messages[0], "status") != "recalled" || len(array(get(snapshot.Messages[0], "parts"))) != 0 {
		t.Fatalf("recall projection changed: %v, %v", snapshot.Messages, err)
	}
}

func BenchmarkDisplayCopies(b *testing.B) {
	for _, size := range []string{"50", "500"} {
		count, _ := strconv.Atoi(size)
		rows := make([]*Object, count)
		scene := fixtureScene(true)
		a := newTestAuthority()
		for i := range rows {
			rows[i] = fixtureMessage(scene.ID, strconv.Itoa(i), strings.Repeat("sample message ", 15))
		}
		b.Run(size+"/two_copies", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				input := make([]*Object, len(rows))
				for i, row := range rows {
					input[i] = cloneJSON(row).(*Object)
				}
				if _, err := a.ProjectDisplay(scene, input, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(size+"/one_copy", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := a.ProjectDisplay(scene, rows, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
