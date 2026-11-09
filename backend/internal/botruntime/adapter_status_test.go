package botruntime

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestAdapterStatusOrderedSnapshot(t *testing.T) {
	r := new(Runtime)
	seen := []string{}
	getter := func(name, status string) *Adapter {
		return &Adapter{Status: func(context.Context) (string, error) { seen = append(seen, name); return status, nil }}
	}
	r.adapters.Store("a", getter("old-a", "connected"))
	r.adapters.Store("b", getter("old-b", "connected"))
	r.adapters.Store("a", getter("a", "connecting"))
	r.adapters.Store("c", getter("c", "invalid"))
	r.adapters.Delete("b")
	r.adapters.Store("b", getter("b", "disposed"))
	got, err := r.AdapterStatuses(context.Background())
	want := []AdapterStatus{{"a", "connecting"}, {"c", "unknown"}, {"b", "disposed"}}
	if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(seen, []string{"a", "a", "c", "b", "b"}) {
		t.Fatalf("%v %v %v", got, err, seen)
	}
}
func TestAdapterStatusGetterMutationKeepsSnapshot(t *testing.T) {
	r := new(Runtime)
	reads := 0
	old := &Adapter{Status: func(context.Context) (string, error) { return "disconnected", nil }}
	r.adapters.Store("a", &Adapter{Status: func(context.Context) (string, error) {
		reads++
		r.adapters.Delete("b")
		r.adapters.Store("b", &Adapter{Status: func(context.Context) (string, error) { return "connected", nil }})
		if reads == 1 {
			return "connected", nil
		}
		return "changed-after-membership", nil
	}})
	r.adapters.Store("b", old)
	got, err := r.AdapterStatuses(context.Background())
	if err != nil || !reflect.DeepEqual(got, []AdapterStatus{{"a", "changed-after-membership"}, {"b", "disconnected"}}) {
		t.Fatalf("%v %v", got, err)
	}
}
func TestAdapterStatusFailureAndCancellation(t *testing.T) {
	r := new(Runtime)
	got, err := r.AdapterStatuses(context.Background())
	if err != nil || got == nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	failure := errors.New("getter-failed")
	r.adapters.Store("x", &Adapter{Status: func(context.Context) (string, error) { return "", failure }})
	if _, err = r.AdapterStatuses(context.Background()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = r.AdapterStatuses(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
