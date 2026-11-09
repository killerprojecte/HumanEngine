package semantic

import (
	"database/sql"
	"errors"
	"sort"
	"sync/atomic"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// ModelReadPolicy is supplied by the trusted composition using the same read
// scope. It is not a tool-provided authorization or a replacement for provenance.
type ModelReadPolicy struct {
	Authority PrivacyAuthority
	Enabled   func(Context) (bool, error)
	NowMillis func() float64
	ParseDate func(string) (float64, error)
}
type ModelReader struct {
	scope  sourceevents.RuntimeReadScope
	ready  func() error
	policy ModelReadPolicy
}

// ReadRuntimeCurrent exposes no mutation methods. Every read borrows the exact
// caller's connection and expires with this callback, including on error.
func (s *Store) ReadRuntimeCurrent(scope sourceevents.RuntimeReadScope, p ModelReadPolicy, fn func(*ModelReader) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session != nil || s.maintenance != nil {
		return errors.New("semantic_unbound_runtime_owner_required")
	}
	if scope.CheckBinding == nil || scope.Ready == nil || scope.Query == nil || fn == nil || p.Authority.AssertReady == nil || p.Authority.Current == nil || p.Authority.AnchorCurrent == nil || p.Authority.PartitionKey == nil || p.Authority.EpisodeAnchor == nil || p.Enabled == nil || p.NowMillis == nil || p.ParseDate == nil {
		return errors.New("semantic_current_owner_required")
	}
	if e := scope.CheckBinding(s.main, s.authority); e != nil {
		return e
	}
	var active atomic.Bool
	active.Store(true)
	defer active.Store(false)
	check := func() error {
		if !active.Load() {
			return sql.ErrTxDone
		}
		if e := scope.CheckBinding(s.main, s.authority); e != nil {
			return e
		}
		return p.Authority.AssertReady()
	}
	reader := &ModelReader{scope: scope, ready: check, policy: p}
	return errors.Join(fn(reader), check())
}
func (r *ModelReader) rows(where string, args ...any) (out []*jsjson.Object, err error) {
	out = []*jsjson.Object{}
	if err = r.ready(); err != nil {
		return
	}
	exists, e := r.scope.Query("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", "memory_semantic_candidates")
	if e != nil {
		return nil, e
	}
	found := exists.Next()
	e = errors.Join(exists.Err(), exists.Close())
	if e != nil || !found {
		return out, e
	}
	rows, e := r.scope.Query("SELECT id,scene_id,input_key,version,value FROM memory_semantic_candidates"+where, semBind(args)...)
	if e != nil {
		return nil, e
	}
	defer func() { err = errors.Join(err, rows.Err(), rows.Close()) }()
	scanned := 0
	for rows.Next() {
		scanned++
		if where == " WHERE scene_id=? ORDER BY id" && scanned > 40 {
			return nil, errors.New("semantic_storage_capacity_invalid")
		}
		var v [5]any
		if e = rows.Scan(&v[0], &v[1], &v[2], &v[3], &v[4]); e != nil {
			return nil, e
		}
		o, e := decodeCandidate(v[0], v[1], v[2], v[3], v[4])
		if e != nil {
			return nil, e
		}
		if o != nil {
			out = append(out, o)
		}
	}
	return
}
func (r *ModelReader) Current(c Context, id string) (*jsjson.Object, error) {
	return r.project(c, id, map[any]string{})
}
func (r *ModelReader) List(c Context, limit int) (*jsjson.Object, error) {
	if e := r.ready(); e != nil {
		return nil, e
	}
	if limit < 1 || limit > 40 {
		return nil, errors.New("semantic_read_limit")
	}
	items := []any{}
	if c.Simulation {
		return privacyObject("items", items, "coverage", "complete"), nil
	}
	enabled, e := r.policy.Enabled(c)
	if e != nil {
		return nil, e
	}
	if !enabled {
		return privacyObject("items", items, "coverage", "complete"), nil
	}
	rows, e := r.rows(" WHERE scene_id=? ORDER BY id", c.SceneID)
	if e != nil {
		return nil, e
	}
	if len(rows) > 40 {
		return nil, errors.New("semantic_storage_capacity_invalid")
	}
	partitions := map[any]string{}
	for _, row := range rows {
		v, e := r.project(c, scStr(scGet(row, "id")), partitions)
		if e != nil {
			return nil, e
		}
		if v != nil {
			items = append(items, v)
		}
	}
	coverage := "complete"
	if len(items) > limit {
		coverage = "limited"
		items = items[:limit]
	}
	return privacyObject("items", items, "coverage", coverage), nil
}
func (r *ModelReader) project(c Context, id string, partitions map[any]string) (*jsjson.Object, error) {
	if e := r.ready(); e != nil {
		return nil, e
	}
	if c.Simulation {
		return nil, nil
	}
	enabled, e := r.policy.Enabled(c)
	if e != nil || !enabled {
		return nil, e
	}
	rows, e := r.rows(" WHERE id=?", id)
	if e != nil || len(rows) == 0 {
		return nil, e
	}
	candidate := rows[0]
	if scGet(candidate, "sceneId") != c.SceneID || scGet(candidate, "status") == "rejected" {
		return nil, nil
	}
	a := r.policy.Authority
	if scGet(candidate, "partitionKey") != nil {
		person := scGet(candidate, "personId")
		key, ok := partitions[person]
		if !ok {
			key, e = a.PartitionKey(c, person)
			if e != nil {
				return nil, e
			}
			partitions[person] = key
		}
		if key != scGet(candidate, "partitionKey") {
			return nil, nil
		}
	}
	deps := []any{}
	for _, raw := range scArr(scGet(candidate, "anchorDependencies")) {
		dep := scObject(raw)
		ref := scObject(scGet(dep, "ref"))
		live, e := a.AnchorCurrent(c, ref)
		if e != nil || live == nil {
			return nil, e
		}
		record := scObject(scGet(live, "record"))
		if scGet(record, "id") != scGet(ref, "id") || scGet(scGet(record, "scope"), "kind") != "scene" || scGet(scGet(record, "scope"), "sceneId") != c.SceneID {
			return nil, nil
		}
		fp, e := Fingerprint(record)
		if e != nil {
			return nil, e
		}
		if fp != scGet(dep, "fingerprint") {
			return nil, nil
		}
		proofs := privacyIntersection(scGet(dep, "proofs"), scGet(live, "proofs"))
		if len(proofs) == 0 {
			return nil, nil
		}
		next := dep.Clone()
		_ = next.Set("proofs", proofs)
		deps = append(deps, next)
	}
	declarations, parents, refs := []any{}, []any{}, []any{}
	allRoots := map[string]bool{}
	for _, raw := range scArr(scGet(candidate, "parents")) {
		parent := scObject(raw)
		ref := scObject(scGet(parent, "ref"))
		live, e := a.Current(c, ref)
		if e != nil || live == nil {
			return nil, e
		}
		record := scObject(scGet(live, "record"))
		roots := scArr(scGet(live, "roots"))
		internal := scGet(record, "internalUse")
		if scGet(record, "id") != scGet(ref, "id") || scGet(scGet(record, "scope"), "kind") != "scene" || scGet(scGet(record, "scope"), "sceneId") != c.SceneID || scGet(live, "derived") == true || (internal != nil && internal != "" && internal != false) || scGet(live, "dependencyKey") != scGet(parent, "dependencyKey") || scGet(live, "rootCoverage") != "complete" || len(roots) == 0 || len(roots) > 128 || len(scArr(scGet(live, "proofs"))) == 0 {
			return nil, nil
		}
		liveRoots := map[string]bool{}
		for _, root := range roots {
			v := scStr(root)
			if !privacyPartition.MatchString(v) {
				return nil, nil
			}
			liveRoots[v] = true
		}
		fp, e := Fingerprint(record)
		if e != nil {
			return nil, e
		}
		proofs := privacyIntersection(scGet(parent, "proofs"), scGet(live, "proofs"))
		if fp != scGet(parent, "fingerprint") || len(proofs) == 0 {
			return nil, nil
		}
		kept := []any{}
		for _, root := range scArr(scGet(parent, "roots")) {
			if liveRoots[scStr(root)] {
				kept = append(kept, root)
				allRoots[scStr(root)] = true
			}
		}
		next := parent.Clone()
		_ = next.Set("proofs", proofs)
		_ = next.Set("roots", kept)
		parents = append(parents, next)
		refs = append(refs, ref)
		declarations = append(declarations, privacyObject("ref", ref.Clone(), "record", record.Clone()))
	}
	if scHas(candidate, "episodeAnchor") {
		ok, e := a.EpisodeAnchor(c, scObject(scGet(candidate, "episodeAnchor")), refs, scStr(scGet(candidate, "episodeMembershipKey")))
		if e != nil || !ok {
			return nil, e
		}
	}
	projected := candidate.Clone()
	_ = projected.Set("parents", parents)
	_ = projected.Set("anchorDependencies", deps)
	rootKeys := []string{}
	for root := range allRoots {
		rootKeys = append(rootKeys, root)
	}
	sort.Strings(rootKeys)
	roots := []any{}
	for _, root := range rootKeys {
		roots = append(roots, root)
	}
	applicability := "current"
	if expiry, ok := scGet(candidate, "applicableUntil").(string); ok && expiry != "" {
		deadline, err := r.policy.ParseDate(expiry)
		if err != nil {
			return nil, err
		}
		if r.policy.NowMillis() >= deadline {
			applicability = "review_needed"
		}
	}
	interpretation := "grouped_declarations_not_observed_event"
	switch scGet(candidate, "kind") {
	case "summary_candidate":
		interpretation = "attributed_summary_not_verified_truth"
	case "duplicate_candidate":
		interpretation = "possible_duplicate_not_merged"
	}
	return privacyObject("candidate", projected, "declarations", declarations, "roots", roots, "applicability", applicability, "interpretation", interpretation), nil
}
