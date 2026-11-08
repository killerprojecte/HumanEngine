package modeldiagnostics

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiagnosticClearRetainsPhysicalCredentialResolution(t *testing.T) {
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	s, e := New(Ports{Enabled: func() bool { return true }, Secrets: func(context.Context) ([]string, error) { close(entered); <-release; return []string{"secret"}, nil }})
	if e != nil {
		t.Fatal(e)
	}
	go func() { returned <- s.Capture(context.Background(), Entry{Output: "secret"}, nil) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = s.CloseAndWait(ctx); !errors.Is(e, context.Canceled) {
		t.Fatalf("physical owner lost: %v", e)
	}
	close(release)
	if e = <-returned; e != nil {
		t.Fatal(e)
	}
	if e = s.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(s.rows) != 0 || len(s.timers) != 0 {
		t.Fatal("late capture restored cleared diagnostics")
	}
}
func TestDiagnosticSourceRetentionAndRedaction(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	s, e := New(Ports{Enabled: func() bool { return true }, Secrets: func(context.Context) ([]string, error) { return []string{"abcdef", "abc"}, nil }, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.CloseAndWait(context.Background())
	message, _ := jsjson.NewObject(jsjson.Member{Key: "role", Value: "user"}, jsjson.Member{Key: "content", Value: "abcdef abc"}, jsjson.Member{Key: "images", Value: []any{"never expose image"}})
	output, _ := jsjson.NewObject(jsjson.Member{Key: "api_key", Value: "arbitrary value"}, jsjson.Member{Key: "text", Value: "abcdef"})
	for i := 0; i < 21; i++ {
		if e = s.Capture(context.Background(), Entry{ModelID: "model", Role: "replyer", Messages: []*jsjson.Object{message}, Output: output}, nil); e != nil {
			t.Fatal(e)
		}
	}
	rows := s.Rows()
	if len(rows) != 20 {
		t.Fatal(len(rows))
	}
	if strings.Contains(rows[0].Input, "abcdef") || strings.Contains(rows[0].Input, "never expose") || !strings.Contains(rows[0].Input, "[image input]") {
		t.Fatal(rows[0].Input)
	}
	if rows[0].Output != `{"api_key":"[redacted]","text":"[redacted]"}` {
		t.Fatal(rows[0].Output)
	}
	now = now.Add(15 * time.Minute)
	if len(s.Rows()) != 0 {
		t.Fatal("expiry boundary")
	}
	s.Clear()
	if len(s.timers) != 0 {
		t.Fatal("clear retained timers")
	}
}
func TestDiagnosticUTF16SliceAndHalfSurrogateSecret(t *testing.T) {
	v, e := clean("😀", []string{jsjson.FromUTF16([]uint16{0xd83d})})
	if e != nil {
		t.Fatal(e)
	}
	expected := append([]uint16{'[', 'r', 'e', 'd', 'a', 'c', 't', 'e', 'd', ']'}, 0xde00)
	if v != jsjson.FromUTF16(expected) {
		t.Fatalf("wrong UTF16 replacement %q", v)
	}
	s, _ := New(Ports{Enabled: func() bool { return true }, Secrets: func(context.Context) ([]string, error) { return nil, nil }})
	defer s.CloseAndWait(context.Background())
	if e = s.Capture(context.Background(), Entry{Output: strings.Repeat("x", 47998) + "😀"}, nil); e != nil {
		t.Fatal(e)
	}
	units, _ := jsjson.UTF16(s.Rows()[0].Output)
	if len(units) != 48000 || units[len(units)-1] != 0xd83d {
		t.Fatal("slice must retain lone high surrogate")
	}
}

func TestClearRejectsCaptureHeldInEnabledAcrossReenable(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var enabled atomic.Bool
	enabled.Store(true)
	var block atomic.Bool
	block.Store(true)
	s, e := New(Ports{Enabled: func() bool {
		value := enabled.Load()
		if block.Swap(false) {
			close(entered)
			<-release
		}
		return value
	}, Secrets: func(context.Context) ([]string, error) { return nil, nil }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.CloseAndWait(context.Background())
	returned := make(chan error, 1)
	go func() { returned <- s.Capture(context.Background(), Entry{Output: "old"}, nil) }()
	<-entered
	enabled.Store(false)
	s.Clear()
	enabled.Store(true)
	close(release)
	if e = <-returned; e != nil {
		t.Fatal(e)
	}
	if len(s.Rows()) != 0 {
		t.Fatal("old enabled observation restored cleared diagnostics")
	}
	if e = s.Capture(context.Background(), Entry{Output: "new"}, nil); e != nil {
		t.Fatal(e)
	}
	rows := s.Rows()
	if len(rows) != 1 || rows[0].Output != `"new"` {
		t.Fatal("new generation was not accepted", rows)
	}
}

func TestDisabledDiagnosticsNeverProjectOrReadSecrets(t *testing.T) {
	var enabled atomic.Bool
	var projected, secrets atomic.Int32
	s, e := New(Ports{Enabled: enabled.Load, Secrets: func(context.Context) ([]string, error) { secrets.Add(1); return nil, nil }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.CloseAndWait(context.Background())
	project := func() (Entry, error) { projected.Add(1); return Entry{Output: "enabled"}, nil }
	if e = s.CaptureLazy(context.Background(), project, nil); e != nil {
		t.Fatal(e)
	}
	if projected.Load() != 0 || secrets.Load() != 0 {
		t.Fatal("disabled capture performed expensive work")
	}
	if e = s.CaptureLazy(context.Background(), func() (Entry, error) { panic("disabled projection executed") }, nil); e != nil {
		t.Fatal(e)
	}
	enabled.Store(true)
	if e = s.CaptureLazy(context.Background(), project, nil); e != nil {
		t.Fatal(e)
	}
	if projected.Load() != 1 || secrets.Load() != 1 || len(s.Rows()) != 1 {
		t.Fatal("enabled capture was not materialized")
	}
}
func TestClearRejectsHeldLazyProjection(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s, e := New(Ports{Enabled: func() bool { return true }, Secrets: func(context.Context) ([]string, error) { return nil, nil }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.CloseAndWait(context.Background())
	returned := make(chan error, 1)
	go func() {
		returned <- s.CaptureLazy(context.Background(), func() (Entry, error) { close(entered); <-release; return Entry{Output: "old"}, nil }, nil)
	}()
	<-entered
	s.Clear()
	close(release)
	if e = <-returned; e != nil {
		t.Fatal(e)
	}
	if len(s.Rows()) != 0 {
		t.Fatal("old projection restored cleared rows")
	}
}
