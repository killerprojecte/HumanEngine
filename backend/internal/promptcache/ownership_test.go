package promptcache

import (
	"errors"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func rows(ids ...string) []Entry {
	out := make([]Entry, len(ids))
	for i, id := range ids {
		out[i] = Entry{id, object(member("id", id), member("text", "fresh"))}
	}
	return out
}

func TestSelectionOwnsSliceButUsesCurrentBodies(t *testing.T) {
	var w Windows
	o := Options{true, 4, 2, 8192}
	old := rows("a", "b")
	selected, err := w.Select("key", old, o, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected[0].Value != old[0].Value {
		t.Fatal("selection copied the current body")
	}
	selected[0] = Entry{"replaced", nil}
	if old[0].ID != "a" {
		t.Fatal("selection shares its array storage")
	}
	// Neither caller mutation nor the returned array can alter captured hashes.
	if err := old[0].Value.(*jsjson.Object).Set("text", "changed after selection"); err != nil {
		t.Fatal(err)
	}
	current := rows("a", "b", "c")
	selected, err = w.Select("key", current, o, nil, nil)
	if err != nil || len(selected) != 3 {
		t.Fatalf("captured membership changed: selected=%v err=%v", selected, err)
	}
	for i := range selected {
		if selected[i].Value != current[i].Value {
			t.Fatal("selection returned a stale body")
		}
	}
}

func TestWindowFailureMutationOrder(t *testing.T) {
	cycle := object(member("id", "a"))
	if err := cycle.Set("cycle", cycle); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		current   []Entry
		authority any
		required  []string
		wantCycle bool
		wantError string
		cleared   bool
	}{
		{name: "authority before duplicates", current: rows("a", "a"), authority: cycle, wantCycle: true},
		{name: "body before duplicates", current: []Entry{{"a", cycle}, {"a", nil}}, wantCycle: true},
		{name: "duplicates preserve membership", current: rows("a", "a"), wantError: "prompt_context_duplicate_message"},
		{name: "missing target before authority", current: rows("a"), authority: cycle, required: []string{"absent"}, wantError: "prompt_context_target_budget_exhausted", cleared: true},
		{name: "empty before authority", authority: cycle, cleared: true},
		{name: "empty required before authority", authority: cycle, required: []string{"absent"}, wantError: "prompt_context_target_budget_exhausted", cleared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w Windows
			o := Options{true, 4, 2, 8192}
			if _, err := w.Select("key", rows("a", "b"), o, nil, nil); err != nil {
				t.Fatal(err)
			}
			before, _ := w.windows.get("key")
			_, err := w.Select("key", tc.current, o, tc.authority, tc.required)
			if tc.wantCycle {
				if !errors.Is(err, jsjson.ErrCycle) {
					t.Fatalf("got %v, want serialization failure before duplicate check", err)
				}
			} else if tc.wantError != "" {
				if err == nil || err.Error() != tc.wantError {
					t.Fatalf("got %v, want %s", err, tc.wantError)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			after, exists := w.windows.get("key")
			if tc.cleared {
				if exists {
					t.Fatal("failure must clear membership")
				}
			} else if !exists || !reflect.DeepEqual(before, after) {
				t.Fatal("serialization or duplicate failure changed membership")
			}
		})
	}
	// The source hashes only maxMessages+1 candidates. An older unencodable
	// body is outside that suffix and must not poison current selection.
	var w Windows
	current := append([]Entry{{"older", cycle}}, rows("a", "b", "c")...)
	if got, err := w.Select("key", current, Options{true, 2, 1, 8192}, nil, nil); err != nil || len(got) != 1 || got[0].ID != "c" {
		t.Fatalf("older body was not excluded: %v %v", got, err)
	}
	if got, err := w.Select("key", []Entry{{"a", cycle}}, Options{}, cycle, []string{"absent"}); err != nil || len(got) != 1 {
		t.Fatalf("disabled branch must bypass hashing and targets: %v %v", got, err)
	}
	if _, exists := w.windows.get("key"); exists {
		t.Fatal("disabled selection must clear membership")
	}
}

func TestFailedPreparationPreservesTicketAndCapturedHashes(t *testing.T) {
	var b Boundaries
	current := rows("a")
	ticket, err := b.Prepare("key", current, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Prepare("key", rows("a", "a"), nil); err == nil || err.Error() != "prompt_context_duplicate_message" {
		t.Fatal(err)
	}
	cycle := object()
	if err := cycle.Set("self", cycle); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Prepare("key", rows("a", "a"), cycle); !errors.Is(err, jsjson.ErrCycle) {
		t.Fatalf("authority serialization must precede duplicate check: %v", err)
	}
	if _, err := b.Prepare("key", []Entry{{"a", cycle}, {"a", nil}}, nil); !errors.Is(err, jsjson.ErrCycle) {
		t.Fatalf("body serialization must precede duplicate check: %v", err)
	}
	ticket.ContextCacheBoundaries[0] = 99
	if err := current[0].Value.(*jsjson.Object).Set("text", "mutated"); err != nil {
		t.Fatal(err)
	}
	ticket.Commit()
	next, err := b.Prepare("key", rows("a", "b"), nil)
	if err != nil || !reflect.DeepEqual(next.ContextCacheBoundaries, []int{0, 1}) {
		t.Fatalf("failed prepare invalidated ticket or caller mutation changed its hashes: %v %v", next, err)
	}
	// A successfully prepared changed prefix invalidates the old success even
	// if that request never commits. Returning to old content cannot revive it.
	if _, err := b.Prepare("key", rows("changed", "b"), nil); err != nil {
		t.Fatal(err)
	}
	next, err = b.Prepare("key", rows("a", "b"), nil)
	if err != nil || !reflect.DeepEqual(next.ContextCacheBoundaries, []int{1}) {
		t.Fatalf("old successful prefix revived: %v %v", next, err)
	}
}
