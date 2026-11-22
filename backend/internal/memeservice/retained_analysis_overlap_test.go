package memeservice

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestImageOnlyObservationSurvivesOneSourceRecall(t *testing.T) {
	f, s, codec, cA, sourceA, b, original := nativeImageFixtureWithLearning(t, false)
	sourceB := fields("sceneId", "s", "messageId", "second-source", "senderId", "second-person")
	cB := Context{SceneID: "s", SourceEvidence: []*Object{fields("sceneId", "s", "messageId", "second-source", "senderId", "second-person", "available", true)}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	arrived, finish := make(chan struct{}, 4), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(finish) }) }
	defer release()
	var calls atomic.Int32
	models := modelFixture(t, codec, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		arrived <- struct{}{}
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		tags := emptyTags()
		set(tags, "intent", []any{"exact-shared-image"})
		set(tags, "selection", fields("usable", true, "shareable", true, "confidence", 0.99, "containsPersonalData", false, "sourceSpecific", false, "containsRealPerson", false))
		modelReply(w, tags)
	})
	rootA, endA, err := f.authority.OperationContext(ctx, f.main)
	if err != nil {
		t.Fatal(err)
	}
	defer endA()
	rootB, endB, err := f.authority.OperationContext(ctx, f.main)
	if err != nil {
		t.Fatal(err)
	}
	defer endB()
	first, cancelA := context.WithCancel(rootA)
	defer cancelA()
	a, other := make(chan error, 1), make(chan error, 1)
	qualify := func(context.Context, *recovery.WriteSession) (bool, error) { return true, nil }
	go func() { a <- s.AnalyzeRetainedSticker(first, cA, original, sourceA, models, codec, qualify) }()
	select {
	case <-arrived:
	case err := <-a:
		t.Fatalf("first analysis exited before model dispatch: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		asset, err := s.Intake(rootB, cB, b, sourceB, true, codec)
		if err == nil {
			err = s.AnalyzeRetainedSticker(rootB, cB, asset, sourceB, models, codec, qualify)
		}
		other <- err
	}()
	waitUntil(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.retainedAnalyses) != 1 {
			return false
		}
		for _, task := range s.retainedAnalyses {
			if len(task.members) != 2 {
				return false
			}
			for member := range task.members {
				if get(member.source, "messageId") == "second-source" {
					return true
				}
			}
		}
		return false
	})
	if _, err := s.Recall(ctx, "s", "m"); err != nil {
		t.Fatal(err)
	}
	cancelA()
	select {
	case err := <-a:
		if err == nil {
			t.Fatal("recalled consumer returned successful analysis")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	endA() // The surviving caller must not inherit the recalled owner's root.
	release()
	select {
	case err := <-other:
		if err != nil {
			t.Fatalf("surviving original source lost shared image analysis: %v (calls=%d)", err, calls.Load())
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	current, err := s.AdminGet(ctx, str(get(original, "id")))
	if err != nil {
		t.Fatal(err)
	}
	sources := arr(get(current, "sources"))
	if calls.Load() != 1 || len(sources) != 1 || get(obj(sources[0]), "messageId") != "second-source" ||
		!yes(get(current, "knownOnly")) || !yes(get(current, "analyzed")) || yes(get(current, "available")) ||
		yes(get(current, "shared")) || yes(get(current, "privacyReviewed")) ||
		stringify(get(obj(get(current, "tags")), "intent")) != `["exact-shared-image"]` {
		t.Fatalf("wrong image-only disposition (calls=%d): %s", calls.Load(), stringify(current))
	}
}

func TestRetainedStickerAnalysisDoesNotOverwriteManualTags(t *testing.T) {
	f, s, codec, c, source, _, original := nativeImageFixtureWithLearning(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	arrived, finish := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(finish) }) }
	defer release()
	models := modelFixture(t, codec, func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		tags := emptyTags()
		set(tags, "intent", []any{"stale-proposal"})
		modelReply(w, tags)
	})
	root, end, err := f.authority.OperationContext(ctx, f.main)
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	done := make(chan error, 1)
	go func() {
		done <- s.AnalyzeRetainedSticker(root, c, original, source, models, codec, func(context.Context, *recovery.WriteSession) (bool, error) { return true, nil })
	}()
	select {
	case <-arrived:
	case err := <-done:
		t.Fatalf("early analysis exit: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	id := str(get(original, "id"))
	tags := emptyTags()
	set(tags, "intent", []any{"manual-current"})
	if _, err := s.Update(root, id, fields("tags", tags)); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stale proposal reported committed")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	actual, err := s.AdminGet(ctx, id)
	if err != nil || stringify(get(obj(get(actual, "tags")), "intent")) != `["manual-current"]` {
		t.Fatalf("manual label overwritten: %s %v", stringify(actual), err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.retainedAnalyses) != 0 {
		t.Fatal("analysis task leaked after stale proposal")
	}
}

type drainingStickerTransport func(*http.Request) (*http.Response, error)

func (f drainingStickerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type stickerAdmissionWait struct {
	context.Context
	reached chan struct{}
	once    sync.Once
}

func (w *stickerAdmissionWait) Done() <-chan struct{} {
	w.once.Do(func() { close(w.reached) })
	return w.Context.Done()
}

func TestRetainedStickerFreshSourceWaitsForCancelledTaskDrain(t *testing.T) {
	f, s, codec, cA, sourceA, b, assetA := nativeImageFixtureWithLearning(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	firstRequest, physicalDrain, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(finish) }) }
	defer release()
	var wireCalls, transports, active atomic.Int32
	var overlap atomic.Bool
	models := modelFixture(t, codec, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if wireCalls.Add(1) == 1 {
			close(firstRequest)
			select {
			case <-r.Context().Done():
			case <-ctx.Done():
			}
			return
		}
		tags := emptyTags()
		set(tags, "intent", []any{"fresh-after-drain"})
		modelReply(w, tags)
	})
	client := models.Client.HTTP
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = drainingStickerTransport(func(r *http.Request) (*http.Response, error) {
		if active.Add(1) != 1 {
			overlap.Store(true)
		}
		defer active.Add(-1)
		first := transports.Add(1) == 1
		response, err := transport.RoundTrip(r)
		if first {
			close(physicalDrain)
			<-finish
		}
		return response, err
	})
	rootA, endA, err := f.authority.OperationContext(ctx, f.main)
	if err != nil {
		t.Fatal(err)
	}
	defer endA()
	rootB, endB, err := f.authority.OperationContext(ctx, f.main)
	if err != nil {
		t.Fatal(err)
	}
	defer endB()
	first, cancelA := context.WithCancel(rootA)
	defer cancelA()
	qualify := func(context.Context, *recovery.WriteSession) (bool, error) { return true, nil }
	a, other := make(chan error, 1), make(chan error, 1)
	go func() { a <- s.AnalyzeRetainedSticker(first, cA, assetA, sourceA, models, codec, qualify) }()
	select {
	case <-firstRequest:
	case <-ctx.Done():
		t.Fatal("first source did not reach real provider")
	}
	sourceB := fields("sceneId", "s", "messageId", "fresh-source", "senderId", "second-person")
	cB := Context{SceneID: "s", SourceEvidence: []*Object{fields("sceneId", "s", "messageId", "fresh-source", "senderId", "second-person", "available", true)}}
	assetB, err := s.Intake(rootB, cB, b, sourceB, true, codec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recall(ctx, "s", "m"); err != nil {
		t.Fatal(err)
	}
	cancelA()
	select {
	case <-physicalDrain:
	case <-ctx.Done():
		t.Fatal("cancel did not enter held physical transport drain")
	}
	key := cA.SceneID + ":" + content(assetA)
	s.mu.Lock()
	old := s.retainedAnalyses[key]
	empty := old != nil && len(old.members) == 0
	s.mu.Unlock()
	if !empty {
		t.Fatal("canceled owner was not still draining")
	}

	wait := &stickerAdmissionWait{Context: rootB, reached: make(chan struct{})}
	go func() { other <- s.AnalyzeRetainedSticker(wait, cB, assetB, sourceB, models, codec, qualify) }()
	select {
	case <-wait.reached:
	case <-ctx.Done():
		t.Fatal("fresh source did not reach admission wait")
	}
	s.mu.Lock()
	stillDraining := s.retainedAnalyses[key] == old && len(old.members) == 0
	s.mu.Unlock()
	if !stillDraining || transports.Load() != 1 || active.Load() != 1 {
		t.Fatalf("fresh source attached to canceled owner or overlapped its drain: transports=%d active=%d", transports.Load(), active.Load())
	}
	before, err := s.AdminGet(ctx, str(get(assetA, "id")))
	if err != nil || yes(get(before, "analyzed")) {
		t.Fatal("canceled source published before draining", err)
	}
	release()
	select {
	case err := <-a:
		if err == nil {
			t.Fatal("canceled source reported success")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	endA()
	select {
	case err := <-other:
		if err != nil {
			t.Fatalf("fresh source inherited canceled task: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	after, err := s.AdminGet(ctx, str(get(assetB, "id")))
	if err != nil {
		t.Fatal(err)
	}
	sources := arr(get(after, "sources"))
	if overlap.Load() || wireCalls.Load() != 2 || transports.Load() != 2 || active.Load() != 0 ||
		len(sources) != 1 || get(obj(sources[0]), "messageId") != "fresh-source" ||
		stringify(get(obj(get(after, "tags")), "intent")) != `["fresh-after-drain"]` {
		t.Fatalf("wrong fresh source disposition (wire=%d overlap=%t): %s", wireCalls.Load(), overlap.Load(), stringify(after))
	}
}
