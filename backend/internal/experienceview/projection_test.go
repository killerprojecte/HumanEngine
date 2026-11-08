package experienceview

import (
	"errors"
	"strings"
	"testing"
)

func snapshot(id string) *Object {
	return obj("ref", obj("id", id, "version", float64(1), "deletionEpoch", float64(0)), "qualificationKey", strings.Repeat("a", 64), "record", obj("id", id, "scope", obj("kind", "scene", "sceneId", "s"), "kind", "experience", "status", "active", "content", "synthetic", "learnedAt", "2026-01-01", "sources", []any{}))
}
func input(a ...any) *Object {
	return obj("sceneId", "s", "simulation", false, "assertions", a, "commitments", []any{}, "upstreamCoverage", obj("assertions", "complete", "commitments", "complete"))
}
func TestProjectionOwnershipOrderCoverageAndDuplicates(t *testing.T) {
	a, b := snapshot("a"), snapshot("b")
	in := input(a, b)
	_ = in.Set("limit", float64(1))
	page, e := Project(in)
	if e != nil {
		t.Fatal(e)
	}
	if get(get(page, "coverage"), "omittedEntries") != float64(1) {
		t.Fatal("coverage")
	}
	_ = get(a, "record").(*Object).Set("content", "mutated")
	item := get(page, "items").([]any)[0]
	if get(get(item, "record"), "content") != "synthetic" {
		t.Fatal("input alias")
	}
	_ = in.Set("assertions", []any{b, b})
	if _, e = Project(in); !errors.Is(e, ErrInvalid) {
		t.Fatal("duplicate outside output limit accepted")
	}
	_ = in.Set("simulation", true)
	if p, e := Project(in); e != nil || len(get(p, "items").([]any)) != 0 {
		t.Fatal("simulation must skip snapshot checks")
	}
	_ = in.Set("limit", float64(13))
	if _, e = Project(in); !errors.Is(e, ErrInvalid) {
		t.Fatal("simulation skipped limit validation")
	}
}
func TestVerifyIncludedOnlyKeyedEqualityAndFailures(t *testing.T) {
	a, b := snapshot("a"), snapshot("b")
	page, e := Project(input(a, b))
	if e != nil {
		t.Fatal(e)
	}
	_ = page.Set("coverage", nil)
	calls := 0
	resolve := func(scene string, refs *Object) (*Object, error) {
		calls++
		if scene != "s" || len(get(refs, "assertions").([]any)) != 2 || len(get(refs, "commitments").([]any)) != 0 {
			t.Fatal("batch")
		}
		return obj("assertions", []any{b, a}, "commitments", []any{}), nil
	}
	if !Verify("s", page, resolve) || calls != 1 {
		t.Fatal("reordered batch/coverage should verify")
	}
	_ = get(b, "record").(*Object).Set("content", "changed")
	if Verify("s", page, resolve) {
		t.Fatal("changed record accepted")
	}
	if Verify("s", page, func(string, *Object) (*Object, error) { panic("callback") }) {
		t.Fatal("panic accepted")
	}
	if !Verify("s", obj("items", []any{}), nil) {
		t.Fatal("empty page needs no callback")
	}
}
