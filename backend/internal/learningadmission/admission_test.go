package learningadmission

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"testing"
)

func setup(t *testing.T) (*Admission, *int64) {
	t.Helper()
	collator, e := topiccollation.New("en-US")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { collator.Close() })
	now := int64(0)
	a, e := New(Options{10, 20, 30, 3, 5, 3}, func() int64 { return now }, collator)
	if e != nil {
		t.Fatal(e)
	}
	return a, &now
}
func TestAdmissionRetryRecallCapacityAndAtomicConfigure(t *testing.T) {
	a, now := setup(t)
	admit := func(id, key string) string {
		s, e := a.Admit(id, key, "background")
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	if admit("a", "1") != "accepted" || admit("a", "1") != "duplicate" {
		t.Fatal("dedup")
	}
	*now = 10
	b, e := a.Take("a", 1, Capacity{})
	if e != nil || b == nil || b.Token != 1 {
		t.Fatal(b, e)
	}
	if admit("a", "1") != "duplicate" {
		t.Fatal("active dedup")
	}
	admit("a", "2")
	retry := int64(5)
	a.Finish("a", 1, &retry)
	if s := a.Inspect("a", Capacity{}); s.DueAt != 40 || s.State != "waiting" {
		t.Fatal(s)
	}
	*now = 40
	b, _ = a.Take("a", 1, Capacity{})
	if b.Token != 2 || b.SourceKeys[0] != "1" {
		t.Fatal(b)
	}
	a.Recall("a", "1", true)
	if s := a.Inspect("a", Capacity{}); s.DueAt != 70 || s.Pending != 1 {
		t.Fatal(s)
	}
	bad := Options{-1, 20, 30, 1, 1, 1}
	if a.Configure(bad) == nil {
		t.Fatal("invalid accepted")
	}
	if admit("a", "3") != "accepted" || admit("a", "4") != "accepted" || admit("a", "5") != "capacity" {
		t.Fatal("capacity")
	}
	a.SetEnabled(false)
	if admit("a", "1") != "disabled" {
		t.Fatal("disabled")
	}
	a.SetEnabled(true)
	if a.Inspect("a", Capacity{}).State != "idle" {
		t.Fatal("replay")
	}
}
func TestAdmissionQuietMaximumAndForeground(t *testing.T) {
	a, now := setup(t)
	a.Admit("a", "1", "background")
	*now = 9
	a.Admit("a", "2", "background")
	*now = 18
	a.Admit("a", "3", "background")
	s := a.Inspect("a", Capacity{})
	if s.DueAt != 20 {
		t.Fatal(s)
	}
	*now = 20
	if a.Inspect("a", Capacity{ForegroundBusy: true}).Reason != "foreground_busy" {
		t.Fatal("foreground")
	}
	if a.Inspect("a", Capacity{BudgetUnavailable: true}).Reason != "budget_unavailable" {
		t.Fatal("budget")
	}
	a.Admit("ä", "1", "background")
	a.Admit("z", "1", "realtime")
	*now = 40
	ready, e := a.ReadyScenes(Capacity{})
	if e != nil || len(ready) != 3 || ready[0] != "z" {
		t.Fatal(ready, e)
	}
}
func TestPatchPreservesOmittedAndExplicitZero(t *testing.T) {
	a, now := setup(t)
	zero := int64(0)
	if e := a.ConfigurePatch(Patch{QuietMS: &zero, MinIntervalMS: &zero}); e != nil {
		t.Fatal(e)
	}
	a.Admit("a", "1", "background")
	if a.Inspect("a", Capacity{}).State != "ready" {
		t.Fatal("explicit zero lost")
	}
	bad := int64(-1)
	if a.ConfigurePatch(Patch{MaxScenes: &bad}) == nil {
		t.Fatal("negative accepted")
	}
	*now = 1
	a.Admit("b", "1", "background")
	if a.Inspect("b", Capacity{}).State != "ready" {
		t.Fatal("invalid patch mutated options")
	}
	if SemanticDefaults() != (Options{30000, 120000, 60000, 128, 1024, 128}) {
		t.Fatal("source defaults changed")
	}
}
func TestAdmissionNumberAdditionMatchesSource(t *testing.T) {
	if numberAdd(1, 9007199254740991) != 9007199254740992 || numberAdd(2, 9007199254740991) != 9007199254740992 {
		t.Fatal("JavaScript rounding changed")
	}
}
