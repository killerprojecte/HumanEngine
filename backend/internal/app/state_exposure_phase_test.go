package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"net/http/httptest"
	"testing"
	"time"
)

// Named independent test phases keep one slow successful turn from consuming
// the next turn's wait budget. The root is bounded by the sum of phase budgets;
// any individual phase deadline also cancels that stable owner lifetime.
type stateExposurePhases struct {
	root         context.Context
	cancel       context.CancelFunc
	current      context.Context
	stop         context.CancelFunc
	stopWatch    func() bool
	name         string
	started      time.Time
	budget       time.Duration
	limit, count int
}

func newStateExposurePhases(parent context.Context, budget time.Duration, limit int) *stateExposurePhases {
	root, cancel := context.WithTimeout(parent, budget*time.Duration(limit))
	return &stateExposurePhases{root: root, cancel: cancel, budget: budget, limit: limit}
}
func (p *stateExposurePhases) Start(name string) error {
	if p.current != nil {
		if p.current.Err() != nil {
			return p.Err()
		}
		p.stopWatch()
		p.stop()
	}
	if p.root.Err() != nil {
		return p.root.Err()
	}
	if p.count >= p.limit {
		return errors.New("state exposure phase count exceeded")
	}
	p.count++
	p.name = name
	p.started = time.Now()
	p.current, p.stop = context.WithTimeout(p.root, p.budget)
	current := p.current
	p.stopWatch = context.AfterFunc(current, func() {
		if errors.Is(current.Err(), context.DeadlineExceeded) {
			p.cancel()
		}
	})
	return nil
}
func (p *stateExposurePhases) Err() error {
	if p.current == nil || p.current.Err() == nil {
		return nil
	}
	return fmt.Errorf("state exposure phase %q elapsed=%s: %w", p.name, time.Since(p.started), p.current.Err())
}
func (p *stateExposurePhases) Close() {
	if p.stopWatch != nil {
		p.stopWatch()
	}
	if p.stop != nil {
		p.stop()
	}
	p.cancel()
}

func TestStateExposurePhaseBudgetsAndCancellation(t *testing.T) {
	t.Run("fresh-stage-does-not-cancel-owner", func(t *testing.T) {
		p := newStateExposurePhases(context.Background(), time.Hour, 2)
		defer p.Close()
		if err := p.Start("first"); err != nil {
			t.Fatal(err)
		}
		first := p.current
		firstDeadline, _ := first.Deadline()
		if err := p.Start("second"); err != nil {
			t.Fatal(err)
		}
		secondDeadline, _ := p.current.Deadline()
		if first.Err() != context.Canceled || p.root.Err() != nil || p.current.Err() != nil || secondDeadline.Before(firstDeadline) {
			t.Fatal("phase renewal changed owner lifetime or reused budget")
		}
		if err := p.Start("third"); err == nil {
			t.Fatal("unbounded additional phase accepted")
		}
	})
	t.Run("deadline-cancels-owner-and-cannot-be-reset", func(t *testing.T) {
		p := newStateExposurePhases(context.Background(), 10*time.Millisecond, 10000)
		defer p.Close()
		if err := p.Start("held-send"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-p.root.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("expired phase left owner lifetime running")
		}
		if p.root.Err() != context.Canceled {
			t.Fatal("phase timeout did not cancel owner before its aggregate bound", p.root.Err())
		}
		if !errors.Is(p.Err(), context.DeadlineExceeded) {
			t.Fatal(p.Err())
		}
		if err := p.Start("hide-timeout"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("expired stage reset", err)
		}
	})
	t.Run("parent-cancel-is-not-renewable", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		p := newStateExposurePhases(parent, time.Hour, 2)
		defer p.Close()
		if err := p.Start("before-cancel"); err != nil {
			t.Fatal(err)
		}
		cancel()
		if !errors.Is(p.Err(), context.Canceled) {
			t.Fatal(p.Err())
		}
		if err := p.Start("after-cancel"); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled owner renewed", err)
		}
	})
}

// Only this multi-phase test uses this helper. Runtime owner lifetime remains
// stable while HTTP work observes the current phase cancellation.
func stateExposureRequest(t *testing.T, ctx context.Context, app *Application, method, path string, body any) any {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer synthetic-token")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
	}
	value, err := jsjson.Parse(response.Body.String())
	if err != nil {
		t.Fatal(err)
	}
	return value
}
