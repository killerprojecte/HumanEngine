package recoverydomains

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Trials struct {
	owner         *recovery.Maintenance
	options       Options
	alreadyLatest map[string]bool
}

func NewTrials(owner *recovery.Maintenance) (*Trials, error) {
	return NewTrialsWithOptions(owner, Options{})
}
func NewTrialsWithOptions(owner *recovery.Maintenance, options Options) (*Trials, error) {
	if owner == nil {
		return nil, errors.New("recovery_maintenance_required")
	}
	return &Trials{owner: owner, options: options, alreadyLatest: map[string]bool{}}, nil
}

type trialRow struct {
	policy, scene string
	version       float64
	receipt       *object
}

var hexHash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var receiptDatetime = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d+)?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$`)

func validID(v any) bool {
	s, ok := v.(string)
	if !ok || s == "" {
		return false
	}
	units, e := jsjson.UTF16(s)
	return e == nil && len(units) <= 512
}
func integer(v any, min float64) bool {
	n, ok := v.(float64)
	return ok && !math.IsNaN(n) && n >= min && n <= 9007199254740991 && math.Trunc(n) == n
}
func exactKeys(o *object, required, optional []string) bool {
	if o == nil {
		return false
	}
	for _, k := range required {
		if _, ok := o.Get(k); !ok {
			return false
		}
	}
	for _, k := range o.Keys() {
		known := false
		for _, x := range append(append([]string{}, required...), optional...) {
			if x == k {
				known = true
				break
			}
		}
		if !known {
			return false
		}
	}
	return true
}
func validateReceipt(t *object) error {
	bad := errors.New("recovery_experience_trial_material_invalid")
	required := strings.Fields("schemaVersion policyId sceneId version policyVersion evidencePolicyVersion policyHash requesterId settingsRevision phase createdAt expiresAt instruction validationId positives")
	if !exactKeys(t, required, []string{"reason"}) || num(get(t, "schemaVersion")) != 1 {
		return bad
	}
	for _, k := range []string{"policyId", "sceneId", "requesterId", "validationId"} {
		if !validID(get(t, k)) {
			return bad
		}
	}
	for _, k := range []string{"version", "policyVersion", "evidencePolicyVersion"} {
		if !integer(get(t, k), 1) {
			return bad
		}
	}
	if !integer(get(t, "settingsRevision"), 0) || !hexHash.MatchString(str(get(t, "policyHash"))) {
		return bad
	}
	if !contains([]any{"trial", "promoted", "suspended", "expired"}, get(t, "phase")) {
		return bad
	}
	if v, ok := t.Get("reason"); ok && !contains([]any{"explicit_instruction_trial", "user_reported_helpfulness", "negative_feedback", "instruction_withdrawn", "expiry", "qualification_changed"}, v) {
		return bad
	}
	if !receiptDatetime.MatchString(str(get(t, "createdAt"))) || !receiptDatetime.MatchString(str(get(t, "expiresAt"))) {
		return bad
	}
	created, e := time.Parse(time.RFC3339Nano, str(get(t, "createdAt")))
	if e != nil {
		return bad
	}
	expires, e := time.Parse(time.RFC3339Nano, str(get(t, "expiresAt")))
	if e != nil || expires.UnixMilli() <= created.UnixMilli() || expires.UnixMilli()-created.UnixMilli() > 7*86400000 {
		return bad
	}
	instruction := obj(get(t, "instruction"))
	if !exactKeys(instruction, []string{"id", "version"}, nil) || !validID(get(instruction, "id")) || !integer(get(instruction, "version"), 1) {
		return bad
	}
	positives, ok := get(t, "positives").([]any)
	if !ok || len(positives) > 2 || str(get(t, "phase")) == "promoted" && len(positives) != 2 {
		return bad
	}
	seen := map[string]map[string]bool{}
	for _, v := range positives {
		p := obj(v)
		if !exactKeys(p, strings.Fields("outcomeId outcomeVersion sourceRoot targetRoot turnId"), nil) || !validID(get(p, "outcomeId")) || !validID(get(p, "turnId")) || !integer(get(p, "outcomeVersion"), 1) || !hexHash.MatchString(str(get(p, "sourceRoot"))) || !hexHash.MatchString(str(get(p, "targetRoot"))) {
			return bad
		}
		for _, k := range []string{"outcomeId", "sourceRoot", "targetRoot", "turnId"} {
			if seen[k] == nil {
				seen[k] = map[string]bool{}
			}
			s := str(get(p, k))
			if seen[k][s] {
				return bad
			}
			seen[k][s] = true
		}
	}
	return nil
}
func trialRows(ctx context.Context, tx *recovery.MaintenanceTx) ([]trialRow, error) {
	var kind string
	e := tx.QueryRowContext(ctx, "SELECT type FROM sqlite_master WHERE name='memory_experience_trials'").Scan(&kind)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if kind != "table" {
		return nil, errors.New("recovery_unknown_experience_trial_schema")
	}
	cols, e := tx.QueryContext(ctx, "PRAGMA table_xinfo(memory_experience_trials)")
	if e != nil {
		return nil, e
	}
	names := []string{}
	for cols.Next() {
		var cid, notnull, pk, hidden int
		var name, typ string
		var d any
		if e = cols.Scan(&cid, &name, &typ, &notnull, &d, &pk, &hidden); e != nil {
			cols.Close()
			return nil, e
		}
		names = append(names, name)
	}
	e = cols.Err()
	cols.Close()
	if e != nil {
		return nil, e
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "policy,scene,value,version" {
		return nil, errors.New("recovery_unknown_experience_trial_schema")
	}
	rows, e := tx.QueryContext(ctx, "SELECT policy,scene,version,value FROM memory_experience_trials")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []trialRow{}
	for rows.Next() {
		var p, s, v, raw any
		if e = rows.Scan(&p, &s, &v, &raw); e != nil {
			return nil, e
		}
		version := 0.0
		switch n := v.(type) {
		case int64:
			version = float64(n)
		case float64:
			version = n
		}
		if !validID(p) || !validID(s) || !integer(version, 1) {
			return nil, errors.New("recovery_experience_trial_material_invalid")
		}
		r := trialRow{policy: str(p), scene: str(s), version: version}
		if raw != nil {
			text, ok := raw.(string)
			if !ok {
				return nil, errors.New("recovery_experience_trial_material_invalid")
			}
			value, e := jsjson.Parse(text)
			if e != nil {
				return nil, e
			}
			r.receipt = obj(value)
			if e = validateReceipt(r.receipt); e != nil {
				return nil, e
			}
			if get(r.receipt, "policyId") != p || get(r.receipt, "sceneId") != s || num(get(r.receipt, "version")) != version {
				return nil, errors.New("recovery_experience_trial_material_invalid")
			}
		}
		result = append(result, r)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if len(result) > 4096 {
		return nil, errors.New("recovery_experience_trial_material_invalid")
	}
	return result, nil
}
func (t *Trials) AssertSupported(ctx context.Context) error {
	return t.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error { _, e := trialRows(ctx, tx); return e })
}
func latestTrials(events []string) ([]*object, error) {
	latest := map[string]*object{}
	order := []string{}
	for _, raw := range events {
		v, e := parseEvent(raw)
		if e != nil {
			return nil, e
		}
		if str(get(v, "kind")) != "experience_trial" {
			continue
		}
		id := str(get(v, "policyId"))
		if latest[id] == nil {
			order = append(order, id)
		}
		latest[id] = v
	}
	result := []*object{}
	for _, id := range order {
		result = append(result, latest[id])
	}
	return result, nil
}
func matchingTrial(rows []trialRow, event *object) *trialRow {
	for i := range rows {
		if rows[i].policy == str(get(event, "policyId")) && rows[i].scene == str(get(event, "sceneId")) {
			return &rows[i]
		}
	}
	return nil
}
func trialAtHead(r *trialRow, event *object, opts Options) (bool, error) {
	if r == nil {
		return false, nil
	}
	if get(event, "afterHash") == nil {
		return r.receipt == nil && r.version == num(get(event, "expectedReceiptVersion"))+1, nil
	}
	if r.receipt == nil || r.version != num(get(event, "nextReceiptVersion")) {
		return false, nil
	}
	h, e := hash(r.receipt, opts)
	return h == str(get(event, "afterHash")), e
}
func (t *Trials) Prepare(ctx context.Context, events []string) error {
	latest, e := latestTrials(events)
	if e != nil {
		return e
	}
	next := map[string]bool{}
	e = t.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		rows, e := trialRows(ctx, tx)
		if e != nil {
			return e
		}
		for _, v := range latest {
			ok, e := trialAtHead(matchingTrial(rows, v), v, t.options)
			if e != nil {
				return e
			}
			if ok {
				next[str(get(v, "policyId"))] = true
			}
		}
		return nil
	})
	if e == nil {
		for k := range next {
			t.alreadyLatest[k] = true
		}
	}
	return e
}

// writeTrial implements the durable receipt mutation, including immutable evidence and CAS.
func writeTrial(ctx context.Context, tx *recovery.MaintenanceTx, next, previous *object, operation string, opts Options) error {
	if next != nil {
		if e := validateReceipt(next); e != nil {
			return e
		}
	}
	identity := next
	if identity == nil {
		identity = previous
	}
	if identity == nil {
		return errors.New("experience_trial_missing")
	}
	rows, e := trialRows(ctx, tx)
	if e != nil {
		return e
	}
	var stored *object
	seen := false
	for _, r := range rows {
		if r.policy == str(get(identity, "policyId")) {
			stored = r.receipt
			seen = true
			break
		}
	}
	var a, b any
	if stored != nil {
		h, e := hash(stored, opts)
		if e != nil {
			return e
		}
		a = h
	}
	if previous != nil {
		h, e := hash(previous, opts)
		if e != nil {
			return e
		}
		b = h
	}
	if a != b {
		return errors.New("experience_trial_version_conflict")
	}
	if next != nil && previous != nil {
		if num(get(next, "version")) != num(get(previous, "version"))+1 || get(next, "sceneId") != get(previous, "sceneId") || get(next, "policyId") != get(previous, "policyId") {
			return errors.New("experience_trial_version_conflict")
		}
		for _, k := range []string{"instruction", "validationId", "policyHash", "requesterId", "settingsRevision", "evidencePolicyVersion", "createdAt", "expiresAt"} {
			a, e := hash(get(next, k), opts)
			if e != nil {
				return e
			}
			b, e := hash(get(previous, k), opts)
			if e != nil {
				return e
			}
			if a != b {
				return errors.New("experience_trial_immutable")
			}
		}
		delta := 0.0
		if operation == "promote" {
			delta = 1
		}
		if num(get(next, "policyVersion")) != num(get(previous, "policyVersion"))+delta {
			return errors.New("experience_trial_version_conflict")
		}
	}
	phase := map[string]string{"start": "trial", "collect": "trial", "promote": "promoted", "suspend": "suspended", "expire": "expired"}[operation]
	if (operation == "start") != (previous == nil) || (operation == "erase") != (next == nil) || next != nil && str(get(next, "phase")) != phase {
		return errors.New("experience_trial_transition_invalid")
	}
	if next != nil && previous == nil {
		if num(get(next, "version")) != 1 {
			return errors.New("experience_trial_version_conflict")
		}
		if seen {
			return errors.New("experience_trial_consumed")
		}
		if len(rows) >= 4096 {
			return errors.New("experience_trial_capacity")
		}
	}
	if e = exec(ctx, tx, "CREATE TABLE IF NOT EXISTS memory_experience_trials(policy TEXT PRIMARY KEY,scene TEXT NOT NULL,version INTEGER NOT NULL,value TEXT)"); e != nil {
		return e
	}
	var raw any
	version := num(get(previous, "version")) + 1
	if next != nil {
		s, e := encode(next)
		if e != nil {
			return e
		}
		raw = s
		version = num(get(next, "version"))
	}
	if previous != nil {
		return exec(ctx, tx, "UPDATE memory_experience_trials SET version=?,value=? WHERE policy=?", version, raw, get(identity, "policyId"))
	}
	return exec(ctx, tx, "INSERT INTO memory_experience_trials VALUES(?,?,?,?)", get(identity, "policyId"), get(identity, "sceneId"), get(identity, "version"), raw)
}
func (t *Trials) Apply(ctx context.Context, raw string) error {
	event, e := parseEvent(raw)
	if e != nil {
		return e
	}
	if str(get(event, "kind")) != "experience_trial" {
		return errors.New("recovery_experience_trial_event_invalid")
	}
	if t.alreadyLatest[str(get(event, "policyId"))] {
		return nil
	}
	return t.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		rows, e := trialRows(ctx, tx)
		if e != nil {
			return e
		}
		row := matchingTrial(rows, event)
		done, e := trialAtHead(row, event, t.options)
		if e != nil || done {
			return e
		}
		var previous *object
		if row != nil {
			previous = row.receipt
		}
		if previous != nil {
			h, e := hash(previous, t.options)
			if e != nil {
				return e
			}
			if h == str(get(event, "afterHash")) {
				return nil
			}
		}
		if previous == nil && get(event, "afterHash") == nil {
			return nil
		}
		if previous == nil || get(previous, "sceneId") != get(event, "sceneId") || get(previous, "version") != get(event, "expectedReceiptVersion") {
			return errors.New("recovery_experience_trial_material_missing")
		}
		h, e := hash(previous, t.options)
		if e != nil {
			return e
		}
		if h != str(get(event, "beforeHash")) {
			return errors.New("recovery_experience_trial_material_missing")
		}
		operation := str(get(event, "operation"))
		var next *object
		if operation != "erase" {
			if operation != "suspend" && operation != "expire" {
				return errors.New("recovery_experience_trial_material_missing")
			}
			next = previous.Clone()
			set(next, "version", get(event, "nextReceiptVersion"))
			set(next, "phase", get(event, "nextPhase"))
			if reason, ok := event.Get("nextReason"); ok {
				set(next, "reason", reason)
			} else {
				next.Delete("reason")
			}
			if e = validateReceipt(next); e != nil {
				return e
			}
			h, e = hash(next, t.options)
			if e != nil {
				return e
			}
			if h != str(get(event, "afterHash")) {
				return errors.New("recovery_experience_trial_material_missing")
			}
		}
		if e = writeTrial(ctx, tx, next, previous, operation, t.options); e != nil {
			return e
		}
		if operation == "erase" {
			if e = exec(ctx, tx, experienceDDL); e != nil {
				return e
			}
			for _, v := range arr(get(previous, "positives")) {
				item := obj(v)
				outcome, e := read(ctx, tx, "SELECT value FROM memory_experience_outcomes WHERE id=?", get(item, "outcomeId"))
				if e != nil {
					return e
				}
				if outcome == nil {
					continue
				}
				if get(outcome, "sceneId") != get(event, "sceneId") || get(obj(get(outcome, "policyRef")), "id") != get(event, "policyId") || get(outcome, "version") != get(item, "outcomeVersion") {
					return errors.New("recovery_experience_trial_evidence_missing")
				}
				if e = eraseOutcome(ctx, tx, outcome); e != nil {
					return e
				}
			}
		}
		return nil
	})
}

// Service-owned trial mutations raise the reader and emit a replay witness;
// the bare ExperienceTrials used by the trial recovery port has no hook.
func recordTrialMutation(ctx context.Context, tx *recovery.MaintenanceTx, next, previous *object, operation string, opts Options) error {
	identity := next
	if identity == nil {
		identity = previous
	}
	event := recovery.ExperienceTrial{Operation: operation, PolicyID: str(get(identity, "policyId")), SceneID: str(get(identity, "sceneId")), ExpectedPolicyVersion: num(get(identity, "policyVersion")), NextPolicyVersion: num(get(identity, "policyVersion"))}
	if previous != nil {
		event.ExpectedPolicyVersion = num(get(previous, "policyVersion"))
		v := num(get(previous, "version"))
		event.ExpectedReceiptVersion = &v
		h, e := hash(previous, opts)
		if e != nil {
			return e
		}
		event.BeforeHash = &h
	}
	if next != nil {
		v := num(get(next, "version"))
		event.NextReceiptVersion = &v
		h, e := hash(next, opts)
		if e != nil {
			return e
		}
		event.AfterHash = &h
		phase := str(get(next, "phase"))
		event.NextPhase = &phase
		if reason := str(get(next, "reason")); reason != "" {
			event.NextReason = &reason
		}
	}
	if e := tx.RequireReader(ctx, 9); e != nil {
		return e
	}
	return tx.RecordReplayEffect(ctx, event)
}
