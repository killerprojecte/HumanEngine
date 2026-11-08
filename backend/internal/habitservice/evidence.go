package habitservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsnumber"
	"math"
	"sort"
)

func InScope(c Context, record *Object) bool {
	scene, present := get(record, "sceneId").(string)
	return str(record, "kind") == "botExpression" && !truthy(get(record, "sceneId")) || !c.Simulation && present && scene == c.SceneID
}
func (s *Service) recalculate(record *Object) error {
	if number(record, "evidenceVersion") != 2 {
		_ = record.Set("weight", float64(0))
		_ = record.Set("confidence", float64(0))
		if str(record, "status") != "invalidated" {
			_ = record.Set("status", "disputed")
		}
		return nil
	}
	now := s.now(s)
	sources, e := requiredSources(record)
	if e != nil {
		return e
	}
	kept := []any{}
	net := 0.0
	for _, v := range sources {
		source := object(v)
		if source == nil {
			return errors.New("habit_source_invalid")
		}
		stamp, present := source.Get("timestamp")
		if present && now-num(stamp) < s.policy.RetentionDays*day {
			kept = append(kept, v)
			net += number(source, "direction") * jsnumber.Pow(2, -math.Max(0, now-num(stamp))/(s.policy.HalfLifeDays*day))
		}
	}
	_ = record.Set("sources", kept)
	_ = record.Set("confidence", math.Min(.75, math.Abs(net)/float64(len(kept)+2)))
	weight := math.Copysign(math.Min(.3, math.Abs(net)*.05), net)
	_ = record.Set("weight", weight)
	if str(record, "status") != "invalidated" {
		status := "active"
		if math.Abs(net) < 1e-9 || str(record, "kind") == "sceneRule" && net < 0 {
			status = "disputed"
		}
		_ = record.Set("status", status)
	}
	return nil
}
func (s *Service) currentEvidenceMany(ctx context.Context, records []*Object) ([]*Object, error) {
	effective := make([]*Object, len(records))
	all := []any{}
	for i, r := range records {
		copy, e := clone(r)
		if e != nil {
			return nil, e
		}
		effective[i] = copy
		sources, e := requiredSources(copy)
		if e != nil {
			return nil, e
		}
		all = append(all, sources...)
	}
	current, e := s.CurrentSources(ctx, all)
	if e != nil {
		return nil, e
	}
	index := 0
	for _, r := range effective {
		kept := []any{}
		for _, source := range arr(get(r, "sources")) {
			if current[index] {
				kept = append(kept, source)
			}
			index++
		}
		_ = r.Set("sources", kept)
		if e = s.recalculate(r); e != nil {
			return nil, e
		}
	}
	return effective, nil
}
func (s *Service) AdminList(ctx context.Context) (out []*Object, err error) {
	out = []*Object{}
	err = s.read(ctx, func(r *readScope) error {
		rows, e := r.query("SELECT value FROM habits ORDER BY rowid")
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var raw string
			if e = rows.Scan(&raw); e != nil {
				return e
			}
			v, e := parseRecord(raw)
			if e != nil {
				return e
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	for _, record := range out {
		if err = s.recalculate(record); err != nil {
			return nil, err
		}
	}
	return
}
func (s *Service) List(ctx context.Context, c Context) ([]*Object, error) {
	records, e := s.AdminList(ctx)
	if e != nil {
		return nil, e
	}
	scoped := []*Object{}
	for _, r := range records {
		if InScope(c, r) {
			scoped = append(scoped, r)
		}
	}
	effective, e := s.currentEvidenceMany(ctx, scoped)
	if e != nil {
		return nil, e
	}
	all := []any{}
	for _, r := range effective {
		all = append(all, arr(get(r, "sources"))...)
	}
	current, e := s.CurrentSources(ctx, all)
	if e != nil {
		return nil, e
	}
	out := []*Object{}
	index := 0
	for _, r := range effective {
		valid := true
		for range arr(get(r, "sources")) {
			valid = valid && current[index]
			index++
		}
		if valid && str(r, "status") == "active" && InScope(c, r) {
			out = append(out, sanitize(r))
		}
	}
	return out, nil
}
func (s *Service) ValidationIdentity(ctx context.Context, c Context, id string) (*string, error) {
	ids, e := s.ValidationIdentities(ctx, c, []string{id})
	return ids[id], e
}
func (s *Service) ValidationIdentities(ctx context.Context, c Context, ids []string) (map[string]*string, error) {
	requested := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			requested = append(requested, id)
		}
	}
	rawValues := make([]*string, len(requested))
	raws := make([]*Object, len(requested))
	present := []*Object{}
	readRaw := func(index int, id string, r *readScope) error {
		raw, found, e := r.raw("SELECT value FROM habits WHERE id=?", sqlText(id))
		if e != nil || !found {
			return e
		}
		rawValues[index] = &raw
		record, e := parseRecord(raw)
		if e != nil {
			return e
		}
		raws[index] = record
		present = append(present, record)
		return nil
	}
	if e := s.read(ctx, func(r *readScope) error {
		for i, id := range requested {
			if e := readRaw(i, id, r); e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		return nil, e
	}
	effective, e := s.currentEvidenceMany(ctx, present)
	if e != nil {
		return nil, e
	}
	all := []any{}
	for _, r := range effective {
		all = append(all, arr(get(r, "sources"))...)
	}
	current, e := s.CurrentSources(ctx, all)
	if e != nil {
		return nil, e
	}
	unchanged := make([]bool, len(requested))
	if e = s.read(ctx, func(r *readScope) error {
		for i, id := range requested {
			raw, found, e := r.raw("SELECT value FROM habits WHERE id=?", sqlText(id))
			if e != nil {
				return e
			}
			unchanged[i] = !found && rawValues[i] == nil || found && rawValues[i] != nil && raw == *rawValues[i]
		}
		return nil
	}); e != nil {
		return nil, e
	}
	result := map[string]*string{}
	ri, si := 0, 0
	for i, id := range requested {
		result[id] = nil
		if raws[i] == nil {
			continue
		}
		record := effective[ri]
		ri++
		valid := true
		for range arr(get(record, "sources")) {
			valid = valid && current[si]
			si++
		}
		if !unchanged[i] || str(record, "status") != "active" || !InScope(c, record) || !valid {
			continue
		}
		stable := record.Clone()
		stable.Delete("weight")
		stable.Delete("confidence")
		identity, e := hash(obj("raw", raws[i], "effective", stable, "policy", s.policyValue()))
		if e != nil {
			return nil, e
		}
		result[id] = &identity
	}
	return result, nil
}
func evidenceFor(c Context, source *Object) *Object {
	for _, v := range c.SourceEvidence {
		e := object(v)
		if str(e, "sceneId") == str(source, "sceneId") && str(e, "messageId") == str(source, "messageId") {
			return e
		}
	}
	return nil
}
func (s *Service) add(ctx context.Context, c Context, record *Object, sources []any, direction float64) error {
	now := s.now(s)
	for _, v := range sources {
		source := object(v)
		evidence := evidenceFor(c, source)
		timestamp := get(evidence, "timestamp")
		stamp := num(timestamp)
		if !finite(timestamp) || stamp > now+300000 || stamp <= math.Max(optionalNumber(record, "sourceCutoff", negativeInfinity()), now-s.policy.RetentionDays*day) {
			continue
		}
		author, e := s.author(ctx, source)
		if e != nil {
			return e
		}
		authorHash := digest(author)
		marks := object(get(record, "authorWatermarks"))
		if stamp <= optionalNumber(marks, authorHash, negativeInfinity()) {
			continue
		}
		existing, err := requiredSources(record)
		if err != nil {
			return err
		}
		var previous *Object
		authors := make([]string, len(existing))
		for i, old := range existing {
			p := object(old)
			key := get(p, "authorKey")
			if key == nil {
				authors[i], e = s.author(ctx, p)
				if e != nil {
					return e
				}
			} else {
				authors[i], _ = key.(string)
			}
			if previous == nil && authors[i] == author {
				previous = p
			}
		}
		if previous != nil {
			pk, e := sourceKey(previous)
			if e != nil {
				return e
			}
			sk, e := sourceKey(source)
			if e != nil {
				return e
			}
			if pk == sk || stamp <= optionalNumber(previous, "timestamp", 0) {
				continue
			}
		}
		remaining := []any{}
		for i, old := range existing {
			if authors[i] != author {
				remaining = append(remaining, old)
			}
		}
		added := source.Clone()
		_ = added.Set("direction", direction)
		_ = added.Set("timestamp", timestamp)
		_ = added.Set("authorKey", author)
		remaining = append(remaining, added)
		_ = record.Set("sources", remaining)
		if marks == nil {
			marks = new(Object)
			_ = record.Set("authorWatermarks", marks)
		}
		_ = marks.Set(authorHash, timestamp)
	}
	marks := object(get(record, "authorWatermarks"))
	entries := marksMembers(marks)
	sort.SliceStable(entries, func(i, j int) bool { return num(entries[i].Value) > num(entries[j].Value) })
	if len(entries) > int(s.policy.MaxSources) {
		cut := optionalNumber(record, "sourceCutoff", 0)
		for _, m := range entries[int(s.policy.MaxSources):] {
			cut = math.Max(cut, num(m.Value))
		}
		_ = record.Set("sourceCutoff", cut)
		entries = entries[:int(s.policy.MaxSources)]
	}
	next := new(Object)
	for _, m := range entries {
		_ = next.Set(m.Key, m.Value)
	}
	_ = record.Set("authorWatermarks", next)
	_ = record.Set("evidenceVersion", float64(2))
	retained, e := requiredSources(record)
	if e != nil {
		return e
	}
	sort.SliceStable(retained, func(i, j int) bool {
		return optionalNumber(object(retained[i]), "timestamp", 0) > optionalNumber(object(retained[j]), "timestamp", 0)
	})
	if len(retained) > int(s.policy.MaxSources) {
		cut := optionalNumber(record, "sourceCutoff", 0)
		for _, v := range retained[int(s.policy.MaxSources):] {
			cut = math.Max(cut, optionalNumber(object(v), "timestamp", 0))
		}
		_ = record.Set("sourceCutoff", cut)
		retained = retained[:int(s.policy.MaxSources)]
	}
	_ = record.Set("sources", retained)
	return s.recalculate(record)
}
