package botruntime

import (
	"errors"
	"slices"
	"testing"
)

func TestUnrecalledBatchRechecksLocalRecallAndSkipsEmpty(t *testing.T) {
	s := new(sceneState)
	scene := Scene{ID: "active"}
	local, late, keep := obj("id", "local"), obj("id", "late"), obj("id", "keep")
	views := map[string]bool{"local": true}
	s.view.Store(&sceneView{scene: scene, recalled: views})
	calls := 0
	r := &Runtime{services: Services{FilterUnrecalled: func(got Scene, rows []*Object) ([]*Object, error) {
		calls++
		if got.ID != scene.ID || !slices.Equal(rows, []*Object{late, keep}) {
			t.Fatalf("wrong source batch: %s %v", got.ID, rows)
		}
		s.view.Store(&sceneView{scene: scene, recalled: map[string]bool{"local": true, "late": true}})
		return rows, nil
	}}}
	rows := []*Object{local, late, keep}
	got, err := r.unrecalled(s, rows)
	if err != nil || !slices.Equal(got, []*Object{keep}) || !slices.Equal(rows, []*Object{local, late, keep}) {
		t.Fatalf("late recall or input ownership lost: %v %v", got, err)
	}
	if got, err = r.unrecalled(s, []*Object{local, late}); err != nil || len(got) != 0 || calls != 1 {
		t.Fatalf("fully recalled slice reached source: %v calls=%d %v", got, calls, err)
	}
	failure := errors.New("synthetic authority failure")
	r.services.FilterUnrecalled = func(Scene, []*Object) ([]*Object, error) { return nil, failure }
	if got, err = r.unrecalled(s, []*Object{keep}); got != nil || err != failure {
		t.Fatalf("source error changed: %v %v", got, err)
	}
}
