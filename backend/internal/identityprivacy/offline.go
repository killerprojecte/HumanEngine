package identityprivacy

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"sort"
)

// OfflineRegistry applies the source registry's exact journaled destructive
// commands. It never reconstructs a link or observation from a witness hash.
type OfflineRegistry struct{ owner *recovery.Maintenance }

func NewOfflineRegistry(owner *recovery.Maintenance) (*OfflineRegistry, error) {
	if owner == nil {
		return nil, errors.New("recovery_maintenance_required")
	}
	return &OfflineRegistry{owner}, nil
}
func (o *OfflineRegistry) read(ctx context.Context, tx *recovery.MaintenanceTx) (*jsjson.Object, error) {
	if e := tx.CheckBinding(ctx, o.owner); e != nil {
		return nil, e
	}
	rows, e := tx.QueryContext(ctx, "SELECT name,type FROM sqlite_master WHERE name LIKE 'memory_identity_%'")
	if e != nil {
		return nil, e
	}
	n := 0
	for rows.Next() {
		var name, kind string
		if e = rows.Scan(&name, &kind); e != nil {
			break
		}
		n++
		if name != "memory_identity_registry" || kind != "table" {
			e = errors.New("recovery_unknown_identity_schema")
			break
		}
	}
	if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
		return nil, e
	}
	if n == 0 {
		return object("format", float64(1), "revision", float64(0), "edges", []any{}, "aliases", []any{}, "operations", []any{}), nil
	}
	if n != 1 {
		return nil, errors.New("recovery_unknown_identity_schema")
	}
	rows, e = tx.QueryContext(ctx, "PRAGMA table_xinfo(memory_identity_registry)")
	if e != nil {
		return nil, e
	}
	columns := []string{}
	for rows.Next() {
		var cid, nn, pk, hidden int
		var name, typ string
		var def any
		if e = rows.Scan(&cid, &name, &typ, &nn, &def, &pk, &hidden); e != nil {
			break
		}
		columns = append(columns, name)
	}
	if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
		return nil, e
	}
	sort.Strings(columns)
	if len(columns) != 2 || columns[0] != "id" || columns[1] != "value" {
		return nil, errors.New("recovery_unknown_identity_schema")
	}
	rows, e = tx.QueryContext(ctx, "SELECT id,value FROM memory_identity_registry")
	if e != nil {
		return nil, e
	}
	n = 0
	var state *jsjson.Object
	for rows.Next() {
		var id, value any
		if e = rows.Scan(&id, &value); e != nil {
			break
		}
		n++
		raw, ok := value.(string)
		if n != 1 || id != int64(1) || !ok {
			e = errors.New("recovery_identity_material_invalid")
			break
		}
		parsed, pe := jsjson.Parse(raw)
		if pe == nil {
			state, pe = parseState(parsed)
		}
		if pe != nil {
			e = errors.New("recovery_identity_material_invalid")
			break
		}
	}
	if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
		return nil, e
	}
	if n != 1 {
		return nil, errors.New("recovery_identity_material_invalid")
	}
	return state, nil
}
func identityEvent(raw string) (*jsjson.Object, error) {
	if e := recovery.ValidateEvent(raw); e != nil {
		return nil, e
	}
	v, e := jsjson.Parse(raw)
	return v.(*jsjson.Object), e
}
func matchesIdentity(state, event *jsjson.Object) bool {
	return get(state, "revision") == get(event, "nextRevision") && hash(state) == get(event, "qualificationHash")
}
func (o *OfflineRegistry) Replay(ctx context.Context, entries []recovery.RecoveryEntry, prefix int64) error {
	var latest, applied *jsjson.Object
	for _, entry := range entries {
		v, e := identityEvent(entry.Payload)
		if e != nil {
			return e
		}
		if get(v, "kind") == "identity_mutation" {
			latest = v
			if entry.Sequence <= prefix {
				applied = v
			}
		}
	}
	skip := false
	if e := o.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		state, e := o.read(ctx, tx)
		if e != nil {
			return e
		}
		if latest == nil {
			if get(state, "revision") != float64(0) {
				return errors.New("recovery_identity_witness_missing")
			}
			skip = true
			return nil
		}
		if matchesIdentity(state, latest) {
			skip = true
			return nil
		}
		if (applied != nil && !matchesIdentity(state, applied)) || (applied == nil && get(state, "revision") != float64(0)) {
			return errors.New("recovery_identity_head_mismatch")
		}
		return nil
	}); e != nil {
		return e
	}
	if skip {
		return nil
	}
	for _, entry := range entries {
		if entry.Sequence <= prefix {
			continue
		}
		v, e := identityEvent(entry.Payload)
		if e != nil {
			return e
		}
		if get(v, "kind") != "identity_mutation" {
			continue
		}
		if e = o.owner.Apply(ctx, 6, func(tx *recovery.MaintenanceTx) error {
			state, e := o.read(ctx, tx)
			if e != nil {
				return e
			}
			if get(state, "revision") != get(v, "expectedRevision") || hash(state) != get(v, "beforeHash") {
				return errors.New("recovery_identity_head_mismatch")
			}
			if e = replayIdentityCommand(state, v); e != nil {
				return e
			}
			if !matchesIdentity(state, v) {
				return errors.New("recovery_identity_head_mismatch")
			}
			if _, e = tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS memory_identity_registry(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL)"); e != nil {
				return e
			}
			raw, e := jsjson.Stringify(state)
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, "INSERT INTO memory_identity_registry(id,value) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", raw)
			return e
		}); e != nil {
			return e
		}
	}
	return nil
}

func endpointLess(a, b *jsjson.Object) bool {
	x, _ := jsjson.UTF16(endpointKey(a))
	y, _ := jsjson.UTF16(endpointKey(b))
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return len(x) < len(y)
}
func identityComponent(state *jsjson.Object, anchor *jsjson.Object) (map[string]bool, error) {
	found := map[string]bool{endpointKey(anchor): true}
	for changed := true; changed; {
		changed = false
		for _, value := range get(state, "edges").([]any) {
			edge := value.([]any)
			a, b := endpointKey(edge[0].(*jsjson.Object)), endpointKey(edge[1].(*jsjson.Object))
			if found[a] || found[b] {
				for _, key := range []string{a, b} {
					if !found[key] {
						found[key] = true
						changed = true
						if len(found) > 32 {
							return nil, errors.New("identity_component_capacity")
						}
					}
				}
			}
		}
	}
	return found, nil
}
func replayIdentityCommand(state, event *jsjson.Object) error {
	operation := get(event, "operation").(string)
	if operation == "link" || operation == "alias_put" {
		return errors.New("recovery_identity_material_missing")
	}
	targets := get(event, "targets").(*jsjson.Object)
	var payload any
	selected := []any{}
	if operation == "unlink" || operation == "split" {
		for _, v := range get(targets, "endpoints").([]any) {
			ep, ok := endpoint(v)
			if !ok {
				return errors.New("identity_partition_invalid")
			}
			selected = append(selected, ep)
		}
		sort.SliceStable(selected, func(i, j int) bool { return endpointLess(selected[i].(*jsjson.Object), selected[j].(*jsjson.Object)) })
		payload = selected
	} else if operation == "alias_remove" {
		payload = object("aliasId", get(targets, "aliasIds").([]any)[0])
	} else {
		source := get(targets, "source").(*jsjson.Object)
		payload = object("sceneId", get(source, "sceneId"), "messageId", get(source, "messageId"))
	}
	revision := get(state, "revision").(float64)
	digest := hash(object("kind", operation, "payload", payload, "expectedRevision", get(event, "expectedRevision")))
	operations := get(state, "operations").([]any)
	for _, value := range operations {
		prior := value.(*jsjson.Object)
		if get(prior, "id") == get(event, "operationId") {
			if get(prior, "hash") != digest {
				return errors.New("identity_operation_conflict")
			}
			return nil
		}
	}
	if len(operations) >= 16384 || revision >= 9007199254740991 {
		return errors.New("identity_capacity")
	}
	switch operation {
	case "unlink":
		kept := []any{}
		for _, edge := range get(state, "edges").([]any) {
			if hash(edge) != hash(selected) {
				kept = append(kept, edge)
			}
		}
		_ = state.Set("edges", kept)
	case "split":
		if len(selected) == 0 || len(selected) > 32 {
			return errors.New("identity_partition_invalid")
		}
		keys := map[string]bool{}
		for _, ep := range selected {
			keys[endpointKey(ep.(*jsjson.Object))] = true
		}
		if len(keys) != len(selected) {
			return errors.New("identity_partition_invalid")
		}
		whole, e := identityComponent(state, selected[0].(*jsjson.Object))
		if e != nil {
			return e
		}
		if len(keys) == len(whole) {
			return errors.New("identity_partition_invalid")
		}
		for key := range keys {
			if !whole[key] {
				return errors.New("identity_partition_invalid")
			}
		}
		kept := []any{}
		for _, value := range get(state, "edges").([]any) {
			edge := value.([]any)
			if keys[endpointKey(edge[0].(*jsjson.Object))] == keys[endpointKey(edge[1].(*jsjson.Object))] {
				kept = append(kept, value)
			}
		}
		_ = state.Set("edges", kept)
	case "alias_remove", "source_recall":
		kept := []any{}
		for _, value := range get(state, "aliases").([]any) {
			alias := value.(*jsjson.Object)
			drop := false
			if operation == "alias_remove" {
				drop = get(alias, "id") == get(payload.(*jsjson.Object), "aliasId")
			} else {
				source := get(alias, "source").(*jsjson.Object)
				p := payload.(*jsjson.Object)
				drop = get(source, "kind") == "observation" && get(source, "sceneId") == get(p, "sceneId") && get(source, "messageId") == get(p, "messageId")
			}
			if !drop {
				kept = append(kept, value)
			}
		}
		_ = state.Set("aliases", kept)
	default:
		return errors.New("recovery_identity_event_invalid")
	}
	_ = state.Set("revision", revision+1)
	operations = append(operations, object("id", get(event, "operationId"), "hash", digest, "revision", revision+1))
	_ = state.Set("operations", operations)
	if len(operations)+len(get(state, "edges").([]any))+len(get(state, "aliases").([]any)) > 16384 {
		return errors.New("identity_revocation_reserve_capacity")
	}
	_, e := parseState(state)
	return e
}
func (o *OfflineRegistry) Verify(ctx context.Context, tx *recovery.MaintenanceTx, events []string) error {
	state, e := o.read(ctx, tx)
	if e != nil {
		return e
	}
	var latest *jsjson.Object
	for _, raw := range events {
		v, e := identityEvent(raw)
		if e != nil {
			return e
		}
		if get(v, "kind") == "identity_mutation" {
			latest = v
		}
	}
	if latest != nil && !matchesIdentity(state, latest) {
		return errors.New("recovery_identity_head_mismatch")
	}
	for _, value := range get(state, "aliases").([]any) {
		source := get(value.(*jsjson.Object), "source").(*jsjson.Object)
		if get(source, "kind") != "observation" {
			continue
		}
		var exists bool
		if e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM recall_tombstones WHERE scene_id=? AND message_id=?)", get(source, "sceneId"), get(source, "messageId")).Scan(&exists); e != nil {
			return e
		}
		if exists {
			return errors.New("recovery_identity_source_cleanup_missing")
		}
	}
	return nil
}

// A root source replay must not invent an identity mutation absent from the
// authority. The identity pass runs first; any remaining observation is an
// exact source error, not an invitation to synthesize a new registry head.
func AssertOfflineSourceClean(ctx context.Context, owner *recovery.Maintenance, tx *recovery.MaintenanceTx, scene, message string) error {
	o, e := NewOfflineRegistry(owner)
	if e != nil {
		return e
	}
	state, e := o.read(ctx, tx)
	if e != nil {
		return e
	}
	for _, value := range get(state, "aliases").([]any) {
		source := get(value.(*jsjson.Object), "source").(*jsjson.Object)
		if get(source, "kind") == "observation" && get(source, "sceneId") == scene && get(source, "messageId") == message {
			return errors.New("recovery_identity_source_cleanup_missing")
		}
	}
	return nil
}
