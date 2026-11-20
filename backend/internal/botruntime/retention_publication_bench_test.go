package botruntime

import (
	"math"
	"testing"
)

func TestPublishedRetentionRecomputesEarliestAfterTrim(t *testing.T) {
	clock := &testClock{now: 3_600_100}
	runtime := Runtime{services: Services{Clock: clock}}
	runtime.settings.Store(&Settings{CacheEnabled: true, CacheHours: 1})
	var history *messageBlock
	for _, at := range []float64{101, math.NaN(), 100, 99, 102} {
		history = appendRecord(history, &messageRecord{receivedAt: at})
	}
	scene := new(sceneState)
	scene.view.Store(&sceneView{history: history, earliest: math.NaN()})
	next := runtime.trimPublished(scene)
	retained := bodyRecords(next.history)
	if len(retained) != 3 || next.earliest != 100 || retained[0].receivedAt != 101 || retained[1].receivedAt != 100 || retained[2].receivedAt != 102 {
		t.Fatalf("unexpected published retention: count=%d earliest=%g", len(retained), next.earliest)
	}
	if got := runtime.trimPublished(scene); got != next {
		t.Fatal("unchanged retention republished history")
	}
	clock.Advance(1)
	if next = runtime.trimPublished(scene); next.history.count != 2 || next.earliest != 101 {
		t.Fatalf("newly expired record retained: %+v", next)
	}
	for _, i := range []int{1, 2, 3} {
		if !history.records[i].retention.discarded.Load() {
			t.Fatalf("record %d was not irrevocably discarded", i)
		}
	}
}

func BenchmarkTrimPublishedFresh500(b *testing.B) {
	var history *messageBlock
	for i := 0; i < 500; i++ {
		history = appendRecord(history, &messageRecord{receivedAt: 1000})
	}
	runtime := Runtime{services: Services{Clock: &testClock{now: 1000}}}
	runtime.settings.Store(&Settings{CacheEnabled: true, CacheHours: 24})
	scene := new(sceneState)
	view := &sceneView{history: history, earliest: 1000}
	scene.view.Store(view)
	b.ReportAllocs()
	for b.Loop() {
		if runtime.trimPublished(scene) != view {
			b.Fatal("fresh view was rebuilt")
		}
	}
}
