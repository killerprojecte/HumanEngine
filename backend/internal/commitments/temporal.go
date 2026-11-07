package commitments

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"sort"
)

func (r *Reader) temporalExists() (exists bool, err error) {
	if err = r.ready(); err != nil {
		return
	}
	rows, err := r.query("SELECT name,type FROM sqlite_master WHERE name IN ('memory_commitment_transitions','memory_commitment_temporal_windows')")
	if err != nil {
		return
	}
	n := 0
	bad := false
	for rows.Next() {
		var name, kind string
		if err = rows.Scan(&name, &kind); err != nil {
			_ = rows.Close()
			return
		}
		n++
		bad = bad || kind != "table"
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return
	}
	if n != 0 && (n != 2 || bad) {
		return false, errors.New("commitment_temporal_schema")
	}
	return n == 2, nil
}
func (r *Reader) AllTemporalIDs() ([]string, error) {
	exists, e := r.temporalExists()
	if e != nil || !exists {
		return []string{}, e
	}
	ids, e := r.strings("SELECT id FROM memory_commitment_transitions UNION SELECT id FROM memory_commitment_temporal_windows")
	if e != nil {
		if errors.Is(e, ErrCorrupt) {
			return nil, ErrTemporalCorrupt
		}
		return nil, e
	}
	for _, id := range ids {
		n, e := jsjson.UTF16Len(id)
		if e != nil || n == 0 || n > 512 {
			return nil, ErrTemporalCorrupt
		}
	}
	return ids, nil
}
func (r *Reader) Timeline(id string) (*Object, error) {
	exists, e := r.temporalExists()
	if e != nil || !exists {
		return nil, e
	}
	var scene, rawValue any
	found, e := r.one("SELECT scene_id,value FROM memory_commitment_temporal_windows WHERE id=?", []any{id}, &scene, &rawValue)
	if e != nil {
		return nil, e
	}
	rows, e := r.query("SELECT scene_id,mutation_version,value FROM memory_commitment_transitions WHERE id=? ORDER BY mutation_version", id)
	if e != nil {
		return nil, e
	}
	type row struct {
		scene   any
		version any
		raw     any
	}
	values := []row{}
	for rows.Next() {
		var v row
		if e = rows.Scan(&v.scene, &v.version, &v.raw); e != nil {
			_ = rows.Close()
			return nil, e
		}
		values = append(values, v)
	}
	if e = errors.Join(rows.Err(), rows.Close()); e != nil {
		return nil, e
	}
	if !found {
		if len(values) > 0 {
			return nil, ErrTemporalCorrupt
		}
		return nil, nil
	}
	raw, ok := rawValue.(string)
	if !ok {
		return nil, ErrTemporalCorrupt
	}
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	window, e := NormalizeWindow(v)
	if e != nil {
		return nil, e
	}
	if get(window, "id") != id || get(window, "sceneId") != scene || len(values) > 64 {
		return nil, ErrTemporalCorrupt
	}
	transitions := []any{}
	for _, r := range values {
		raw, ok := r.raw.(string)
		if !ok {
			return nil, ErrTemporalCorrupt
		}
		version, ok := sqliteNumber(r.version)
		if !ok {
			return nil, ErrTemporalCorrupt
		}
		v, e := jsjson.Parse(raw)
		if e != nil {
			return nil, e
		}
		t, e := NormalizeTransition(v)
		if e != nil {
			return nil, e
		}
		if get(t, "id") != id || get(t, "sceneId") != scene || r.scene != scene || get(t, "mutationVersion") != version {
			return nil, ErrTemporalCorrupt
		}
		transitions = append(transitions, t)
	}
	return NormalizeTimeline(obj("window", window, "transitions", transitions))
}
func NormalizeTimeline(v any) (*Object, error) {
	w, e := NormalizeWindow(get(v, "window"))
	if e != nil {
		return nil, e
	}
	raw, ok := get(v, "transitions").([]any)
	if !ok {
		return nil, ErrSchema
	}
	transitions := []any{}
	for _, v := range raw {
		t, e := NormalizeTransition(v)
		if e != nil {
			return nil, e
		}
		transitions = append(transitions, t)
	}
	seen := map[float64]bool{}
	for _, t := range transitions {
		mv := num(get(t, "mutationVersion"))
		if seen[mv] || get(t, "id") != get(w, "id") || get(t, "sceneId") != get(w, "sceneId") || mv <= num(get(w, "prunedThroughMutationVersion")) || mv < num(get(w, "firstMutationVersion")) {
			return nil, ErrTemporalCorrupt
		}
		seen[mv] = true
	}
	if len(transitions) == 0 || len(transitions) > 64 {
		return nil, ErrTemporalCorrupt
	}
	sort.SliceStable(transitions, func(i, j int) bool {
		return num(get(transitions[i], "mutationVersion")) < num(get(transitions[j], "mutationVersion"))
	})
	return obj("window", w, "transitions", transitions), nil
}
func AppendTimeline(previous *Object, raw *Object, parser *jsdate.Parser) (*Object, error) {
	next, e := NormalizeTransition(raw)
	if e != nil {
		return nil, e
	}
	all := append([]any{}, arr(get(previous, "transitions"))...)
	for _, t := range all {
		if num(get(t, "mutationVersion")) >= num(get(next, "mutationVersion")) {
			return nil, errors.New("commitment_temporal_version_conflict")
		}
	}
	all = append(all, next)
	var w *Object
	if previous != nil {
		w = object(get(previous, "window")).Clone()
	} else {
		w = obj("schemaVersion", float64(1), "id", get(next, "id"), "sceneId", get(next, "sceneId"), "firstMutationVersion", get(next, "mutationVersion"), "firstRecordedAt", get(next, "recordedAt"), "prunedThroughMutationVersion", float64(0), "clockNonmonotonic", false)
	}
	if len(all) > 64 {
		_ = w.Set("prunedThroughMutationVersion", get(all[len(all)-65], "mutationVersion"))
	}
	prior := arr(get(previous, "transitions"))
	if len(prior) > 0 {
		a, valid, e := parser.ParseMillis(str(get(next, "recordedAt")))
		if e != nil || !valid {
			return nil, ErrSchema
		}
		b, valid, e := parser.ParseMillis(str(get(prior[len(prior)-1], "recordedAt")))
		if e != nil || !valid {
			return nil, ErrSchema
		}
		if a < b {
			_ = w.Set("clockNonmonotonic", true)
		}
	}
	if len(all) > 64 {
		all = all[len(all)-64:]
	}
	return NormalizeTimeline(obj("window", w, "transitions", all))
}

// ReplaceTimeline deliberately does not introduce a savepoint around lazy DDL.
// A caught SQL failure can retain earlier statements, exactly as the source.
func (s *Store) ReplaceTimeline(id string, timeline *Object) error {
	if e := s.ready(); e != nil {
		return e
	}
	if timeline != nil {
		var e error
		timeline, e = NormalizeTimeline(timeline)
		if e != nil {
			return e
		}
		if get(get(timeline, "window"), "id") != id {
			return ErrTemporalCorrupt
		}
	} else {
		exists, e := s.reader().temporalExists()
		if e != nil || !exists {
			return e
		}
	}
	if e := s.ensureTemporalSchema(); e != nil {
		return e
	}
	if e := s.exec("DELETE FROM memory_commitment_transitions WHERE id=?", id); e != nil {
		return e
	}
	if e := s.exec("DELETE FROM memory_commitment_temporal_windows WHERE id=?", id); e != nil {
		return e
	}
	if timeline == nil {
		return nil
	}
	window := get(timeline, "window")
	raw, e := jsjson.Stringify(window)
	if e != nil {
		return e
	}
	if e = s.exec("INSERT INTO memory_commitment_temporal_windows VALUES(?,?,?)", id, get(window, "sceneId"), raw); e != nil {
		return e
	}
	for _, t := range arr(get(timeline, "transitions")) {
		raw, e := jsjson.Stringify(t)
		if e != nil {
			return e
		}
		if e = s.exec("INSERT INTO memory_commitment_transitions VALUES(?,?,?,?)", id, get(t, "mutationVersion"), get(t, "sceneId"), raw); e != nil {
			return e
		}
	}
	return nil
}

type evidenceTransform func(*Object) (*Object, error)

func mapEvidence(items []any, transform evidenceTransform) ([]any, error) {
	out := []any{}
	for _, v := range items {
		e, err := transform(object(v))
		if err != nil {
			return nil, err
		}
		if e != nil {
			out = append(out, e)
		}
	}
	return out, nil
}
func transformGroups(v any, transform evidenceTransform) ([]any, error) {
	out := []any{}
	for _, g := range arr(v) {
		a, e := mapEvidence(arr(g), transform)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, nil
}

// QualifyRecordedState preserves the recorded declaration. Losing any original
// kind/person declaration, or one inherited AND group, redacts it entirely.
func QualifyRecordedState(state *Object, transform func(*Object) (*Object, error)) (*Object, error) {
	evidence, e := mapEvidence(arr(get(state, "evidence")), transform)
	if e != nil {
		return nil, e
	}
	groups, e := transformGroups(get(state, "requiredProposalGroups"), transform)
	if e != nil {
		return nil, e
	}
	for _, old := range arr(get(state, "evidence")) {
		found := false
		for _, item := range evidence {
			if get(item, "kind") == get(old, "kind") && get(item, "personId") == get(old, "personId") {
				found = true
				break
			}
		}
		if !found {
			return nil, nil
		}
	}
	for _, g := range groups {
		if len(arr(g)) == 0 {
			return nil, nil
		}
	}
	next := state.Clone()
	_ = next.Set("evidence", evidence)
	_ = next.Set("requiredProposalGroups", groups)
	return next, nil
}
func transformTemporal(timeline *Object, transform evidenceTransform) (*Object, error) {
	if timeline == nil {
		return nil, nil
	}
	rows := []any{}
	for _, v := range arr(get(timeline, "transitions")) {
		row := object(v)
		if get(row, "states") == nil {
			rows = append(rows, row)
			continue
		}
		states := []any{}
		redacted := false
		for _, state := range arr(get(row, "states")) {
			next, e := QualifyRecordedState(object(state), transform)
			if e != nil {
				return nil, e
			}
			if next == nil {
				redacted = true
			}
			states = append(states, next)
		}
		row = row.Clone()
		if redacted {
			_ = row.Set("states", nil)
		} else {
			_ = row.Set("states", states)
		}
		rows = append(rows, row)
	}
	return NormalizeTimeline(obj("window", get(timeline, "window"), "transitions", rows))
}
