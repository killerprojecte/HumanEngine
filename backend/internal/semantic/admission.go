package semantic

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
	"sort"
	"time"
)

type AdmissionPolicy struct {
	Enabled, LearningEnabled, AutoSummary bool
	Now                                   func() time.Time
	ID                                    func() (string, error)
}
type Rejection struct {
	Index  int
	Reason string
}
type AdmissionResult struct {
	Created  []*jsjson.Object
	Rejected []Rejection
}
type TrustedEpisode struct {
	Anchor                       *jsjson.Object
	MembershipKey                string
	Dependencies, Qualifications []any
}

func freshID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func resolveAdmission(a PrivacyAuthority, c Context, s *jsjson.Object) (*jsjson.Object, error) {
	v, e := a.Current(c, scObject(scGet(s, "ref")))
	if e != nil || v == nil {
		return nil, e
	}
	r := scObject(scGet(v, "record"))
	scope := scObject(scGet(r, "scope"))
	roots := scArr(scGet(v, "roots"))
	if scGet(r, "id") != scGet(scGet(s, "ref"), "id") || scGet(scope, "kind") != "scene" || scGet(scope, "sceneId") != c.SceneID || admissionTruthy(scGet(v, "derived")) || admissionTruthy(scGet(r, "internalUse")) || scGet(v, "dependencyKey") != scGet(s, "dependencyKey") || scGet(v, "rootCoverage") != "complete" || len(roots) == 0 || len(roots) > 128 || len(scArr(scGet(v, "proofs"))) == 0 {
		return nil, nil
	}
	for _, root := range roots {
		if !inputHash(root) {
			return nil, nil
		}
	}
	return v, nil
}
func sortedRoots(values []any) []any {
	m := map[string]bool{}
	for _, v := range values {
		m[v.(string)] = true
	}
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []any{}
	for _, k := range keys {
		out = append(out, k)
	}
	return out
}

// Admit plans every proposal before writing any candidate. Typed rejection is
// not a transaction error; storage/checkpoint failures are, and must roll back.
func (s *Store) Admit(c Context, delivered, inputs []any, trusted *TrustedEpisode, a PrivacyAuthority, p AdmissionPolicy) (AdmissionResult, error) {
	out := AdmissionResult{Created: []*jsjson.Object{}, Rejected: []Rejection{}}
	if e := s.RequireTransaction(); e != nil {
		return out, e
	}
	reject := func(index int, reason string) (AdmissionResult, error) {
		return AdmissionResult{Created: []*jsjson.Object{}, Rejected: []Rejection{{index, reason}}}, nil
	}
	if c.Simulation || !p.Enabled || !p.LearningEnabled {
		return reject(0, "semantic_disabled")
	}
	if len(delivered) < 2 || len(delivered) > 12 || len(inputs) < 1 || len(inputs) > 3 {
		return reject(0, "semantic_input_limit")
	}
	selections := []*jsjson.Object{}
	seen := map[string]bool{}
	for _, v := range delivered {
		parsed, e := scSelection(v)
		if e != nil {
			return reject(0, "semantic_invalid_candidate")
		}
		o := scObject(parsed)
		id := scStr(scGet(scGet(o, "ref"), "id"))
		if seen[id] {
			return reject(0, "semantic_duplicate_parent")
		}
		seen[id] = true
		selections = append(selections, o)
	}
	views := []*jsjson.Object{}
	people := map[any]bool{}
	parents := []any{}
	for _, selection := range selections {
		view, e := resolveAdmission(a, c, selection)
		if e != nil {
			return out, e
		}
		if view == nil {
			return reject(0, "semantic_parent_unavailable")
		}
		views = append(views, view)
		record := scObject(scGet(view, "record"))
		people[scGet(scGet(record, "scope"), "personId")] = true
		fp, e := Fingerprint(record)
		if e != nil {
			return out, e
		}
		parents = append(parents, inputObject("ref", scGet(selection, "ref"), "dependencyKey", scGet(selection, "dependencyKey"), "fingerprint", fp, "proofs", scGet(view, "proofs"), "roots", sortedRoots(scArr(scGet(view, "roots")))))
	}
	var person any
	if len(people) == 1 {
		for v := range people {
			person = v
		}
	}
	partition := ""
	partitionRead := false
	pending := []*jsjson.Object{}
	for index, raw := range inputs {
		proposal, e := ValidateProposal(scObject(raw))
		if e != nil {
			return reject(index, "semantic_invalid_candidate")
		}
		kind := scStr(scGet(proposal, "kind"))
		if len(people) != 1 && kind != "episode_candidate" {
			return reject(index, "semantic_subject_partition")
		}
		for _, id := range scArr(scGet(proposal, "selectedParentIds")) {
			if !seen[id.(string)] {
				return reject(index, "semantic_parent_not_exposed")
			}
		}
		anchor := scObject(scGet(proposal, "episodeAnchor"))
		dependencies, qualifications := []any{}, []any{}
		if anchor != nil {
			if trusted == nil || !privacyEqual(trusted.Anchor, anchor) || !inputHash(trusted.MembershipKey) {
				return reject(index, "semantic_episode_membership_required")
			}
			refs := []any{}
			for _, p := range parents {
				refs = append(refs, scGet(p, "ref"))
			}
			ok, e := a.EpisodeAnchor(c, anchor, refs, trusted.MembershipKey)
			if e != nil {
				return out, e
			}
			if !ok {
				return reject(index, "semantic_episode_unverified")
			}
			if len(trusted.Qualifications) == 0 || len(trusted.Qualifications) > 40 {
				return reject(index, "semantic_anchor_qualifications_required")
			}
			ids := map[any]bool{}
			for _, q := range trusted.Qualifications {
				id := scGet(q, "edgeId")
				if ids[id] {
					return reject(index, "semantic_anchor_qualifications_required")
				}
				ids[id] = true
			}
			for _, q := range trusted.Qualifications {
				v, e := scQualification(q)
				if e != nil {
					return reject(index, "semantic_anchor_qualifications_invalid")
				}
				qualifications = append(qualifications, v)
			}
			if len(trusted.Dependencies) == 0 || len(trusted.Dependencies) > 128 {
				return reject(index, "semantic_anchor_dependencies_required")
			}
			for _, d := range trusted.Dependencies {
				v, e := scAnchorDependency(d)
				if e != nil {
					return reject(index, "semantic_anchor_dependencies_invalid")
				}
				dep := scObject(v)
				live, e := a.AnchorCurrent(c, scObject(scGet(dep, "ref")))
				if e != nil {
					return out, e
				}
				if live == nil {
					return reject(index, "semantic_anchor_unavailable")
				}
				record := scObject(scGet(live, "record"))
				fp, e := Fingerprint(record)
				if e != nil {
					return out, e
				}
				if scGet(record, "id") != scGet(scGet(dep, "ref"), "id") || scGet(scGet(record, "scope"), "kind") != "scene" || scGet(scGet(record, "scope"), "sceneId") != c.SceneID || fp != scGet(dep, "fingerprint") || len(privacyIntersection(scGet(dep, "proofs"), scGet(live, "proofs"))) != len(scArr(scGet(dep, "proofs"))) {
					return reject(index, "semantic_anchor_unavailable")
				}
				dependencies = append(dependencies, dep.Clone())
			}
		}
		var partitionKey any
		if kind != "episode_candidate" {
			if !partitionRead {
				partition, e = a.PartitionKey(c, person)
				if e != nil {
					return out, e
				}
				partitionRead = true
			}
			if !inputHash(partition) {
				return reject(index, "semantic_partition_unavailable")
			}
			partitionKey = partition
		}
		var membership any
		if anchor != nil {
			membership = trusted.MembershipKey
		}
		key, e := privacyHash([]any{c.SceneID, parents, kind, scGet(proposal, "selectedParentIds"), nullableObject(anchor), membership, partitionKey, dependencies, qualifications, float64(1)})
		if e != nil {
			return out, e
		}
		seenStored, e := s.Seen(key)
		if e != nil {
			return out, e
		}
		if seenStored {
			continue
		}
		for _, v := range pending {
			if scGet(v, "inputKey") == key {
				return reject(index, "semantic_duplicate_proposal")
			}
		}
		auto := kind == "summary_candidate" && scGet(proposal, "hypothesis") == false && p.AutoSummary
		for _, v := range views {
			record := scObject(scGet(v, "record"))
			k := scGet(record, "kind")
			ep := scGet(record, "epistemicKind")
			auto = auto && (k == "fact" || k == "claim" || k == "term") && (ep == "explicit" || ep == "observed") && scGet(record, "status") == "active"
		}
		now := time.Now()
		if p.Now != nil {
			now = p.Now()
		}
		id := freshID
		if p.ID != nil {
			id = p.ID
		}
		candidateID, e := id()
		if e != nil {
			return out, e
		}
		status, admission := "candidate", "qualified_candidate"
		if auto {
			status, admission = "admitted", "structural_low_risk_summary"
		}
		var until any
		if scGet(proposal, "hypothesis") == true {
			until = now.Add(7 * 24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
		}
		candidate := inputObject("schemaVersion", float64(1), "id", candidateID, "sceneId", c.SceneID, "personId", person, "partitionKey", partitionKey, "anchorDependencies", dependencies, "anchorQualifications", qualifications, "version", float64(1), "inputKey", key, "kind", kind, "text", scGet(proposal, "text"), "selectedParentIds", scGet(proposal, "selectedParentIds"), "parents", parents)
		if anchor != nil {
			_ = candidate.Set("episodeAnchor", anchor)
			_ = candidate.Set("episodeMembershipKey", membership)
		}
		for _, pair := range []struct {
			k string
			v any
		}{{"hypothesis", scGet(proposal, "hypothesis")}, {"status", status}, {"admission", admission}, {"createdAt", now.UTC().Format("2006-01-02T15:04:05.000Z")}, {"applicableUntil", until}} {
			_ = candidate.Set(pair.k, pair.v)
		}
		if _, e = ValidateCandidate(candidate); e != nil {
			return out, e
		}
		pending = append(pending, candidate)
	}
	slots, e := s.AvailableSlots(c.SceneID)
	if e != nil {
		return out, e
	}
	if len(pending) > slots {
		return reject(len(inputs), "semantic_capacity")
	}
	for _, candidate := range pending {
		if e = s.Write(candidate, nil, "admit"); e != nil {
			return out, e
		}
		out.Created = append(out.Created, candidate.Clone())
	}
	return out, nil
}
func (s *Store) BeginAttempt(c Context, key string, delivered []any, a PrivacyAuthority, p AdmissionPolicy) (bool, error) {
	if e := s.RequireTransaction(); e != nil {
		return false, e
	}
	if c.Simulation || !p.Enabled || !p.LearningEnabled {
		return false, nil
	}
	if !inputHash(key) || len(delivered) < 2 || len(delivered) > 12 {
		return false, errors.New("semantic_attempt_invalid")
	}
	rows, e := s.Attempts()
	if e != nil {
		return false, e
	}
	for _, r := range rows {
		if scGet(r, "inputKey") == key {
			return false, nil
		}
	}
	if len(rows) >= 4096 {
		return false, nil
	}
	parents, roots := []any{}, []any{}
	seen := map[string]bool{}
	for _, v := range delivered {
		parsed, e := scSelection(v)
		if e != nil {
			return false, e
		}
		p := scObject(parsed)
		id := scStr(scGet(scGet(p, "ref"), "id"))
		if seen[id] {
			return false, errors.New("semantic_duplicate_parent")
		}
		seen[id] = true
		parents = append(parents, p)
	}
	for _, p := range parents {
		v, e := resolveAdmission(a, c, scObject(p))
		if e != nil || v == nil {
			return false, e
		}
		roots = append(roots, scArr(scGet(v, "roots"))...)
	}
	attempt := inputObject("schemaVersion", float64(1), "sceneId", c.SceneID, "inputKey", key, "status", "started", "version", float64(1), "parents", parents, "roots", sortedRoots(roots))
	if e = s.WriteAttempt(attempt, nil); e != nil {
		return false, e
	}
	return true, nil
}
func (s *Store) FinishAttempt(c Context, key, status string) error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	if c.Simulation {
		return nil
	}
	if status != "finished" && status != "abandoned" {
		return errors.New("semantic_attempt_invalid")
	}
	rows, e := s.Attempts()
	if e != nil {
		return e
	}
	for _, previous := range rows {
		if scGet(previous, "inputKey") != key || scGet(previous, "sceneId") != c.SceneID {
			continue
		}
		if scGet(previous, "status") != "started" {
			return nil
		}
		next := previous.Clone()
		_ = next.Set("status", status)
		_ = next.Set("version", semNumber(previous, "version")+1)
		return s.WriteAttempt(next, previous)
	}
	return errors.New("semantic_attempt_missing")
}

func admissionTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	}
	return true
}
