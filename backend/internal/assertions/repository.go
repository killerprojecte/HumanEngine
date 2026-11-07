package assertions

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// Mutations are explicit transaction-local revision/projection effects. A missing
// production integration is an error, never a silent no-op callback.
type Mutations struct {
	BeforeWrite      func(string) error
	AfterWrite       func(*Object) error
	RemoveProjection func(string) error
}
type Repository struct {
	store     *Store
	mutations Mutations
}

func NewRepository(store *Store, mutations Mutations) (*Repository, error) {
	if store == nil || mutations.BeforeWrite == nil || mutations.RemoveProjection == nil {
		return nil, errors.New("assertion_mutations_required")
	}
	return &Repository{store, mutations}, nil
}

// Get preserves the raw JSON root. found distinguishes an absent row from JSON null.
func (r *Reader) Get(id string) (value any, found bool, err error) {
	if err = r.ready(); err != nil {
		return
	}
	var raw string
	found, err = r.supportOne(supportRecordSQL, id, &raw)
	if err != nil || !found {
		return
	}
	value, err = jsjson.Parse(raw)
	return
}
func (r *Reader) Supports(id string) (any, error) {
	var raw string
	found, e := r.supportOne(supportProofSQL, id, &raw)
	if e != nil {
		return nil, e
	}
	if !found {
		return []any{}, nil
	}
	return jsjson.Parse(raw)
}
func (r *Reader) All() (out []any, err error) {
	if err = r.ready(); err != nil {
		return
	}
	out = []any{}
	rows, err := r.query("SELECT value FROM memories ORDER BY rowid DESC")
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return
		}
		v, e := jsjson.Parse(raw)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	err = rows.Err()
	return
}

// Record and Proofs are explicitly typed consumer adapters, not raw storage
// reads. Falsy preimages behave as absent for revision coordination, allowing
// the source's null/false/zero/empty-string row repair path.
func (r *Reader) Record(id string) (*Object, error) {
	v, _, e := r.Get(id)
	if e != nil {
		return nil, e
	}
	if !truthy(v) {
		return nil, nil
	}
	o, ok := v.(*Object)
	if !ok {
		return nil, errors.New("assertion_record_object_required")
	}
	return o, nil
}
func (r *Reader) Proofs(id string) ([]any, error) {
	v, e := r.Supports(id)
	if e != nil {
		return nil, e
	}
	a, ok := v.([]any)
	if !ok {
		return nil, errors.New("assertion_supports_array_required")
	}
	return a, nil
}
func (r *Repository) SaveSupports(id string, proofs []any) error {
	if e := r.mutations.BeforeWrite(id); e != nil {
		return e
	}
	unique := []any{}
	positions := map[string]int{}
	for _, v := range proofs {
		proof, ok := v.(*Object)
		if !ok {
			return errors.New("assertion_proof_object_required")
		}
		key, e := CanonicalProofKey(proof)
		if e != nil {
			return e
		}
		if pos, present := positions[key]; present {
			prior := unique[pos].(*Object)
			combined := prior.Clone()
			observations := append([]any{}, array(get(proof, "sourceObservations"))...)
			observations = append(observations, array(get(prior, "sourceObservations"))...)
			merged := []any{}
			indices := map[string]int{}
			for _, observation := range observations {
				k, e := jsjson.Stringify([]any{get(observation, "sceneId"), get(observation, "messageId"), get(observation, "senderId")})
				if e != nil {
					return e
				}
				if i, ok := indices[k]; ok {
					merged[i] = observation
				} else {
					indices[k] = len(merged)
					merged = append(merged, observation)
				}
			}
			_ = combined.Set("sourceObservations", merged)
			unique[pos] = combined
		} else {
			positions[key] = len(unique)
			unique = append(unique, proof)
		}
	}
	raw, e := jsjson.Stringify(unique)
	if e != nil {
		return e
	}
	return r.store.exec("INSERT INTO memory_supports VALUES(?,?) ON CONFLICT(memory_id) DO UPDATE SET value=excluded.value", id, raw)
}

func (r *Repository) Write(record *Object) error {
	id := str(get(record, "id"))
	if e := r.mutations.BeforeWrite(id); e != nil {
		return e
	}
	var previousFingerprint, previousRaw string
	exists, e := r.store.reader().one("SELECT fingerprint,value FROM memories WHERE id=?", []any{id}, &previousFingerprint, &previousRaw)
	if e != nil {
		return e
	}
	raw, e := jsjson.Stringify(record)
	if e != nil {
		return e
	}
	if exists && previousRaw != raw {
		if e = r.mutations.RemoveProjection(id); e != nil {
			return e
		}
	}
	var fingerprint string
	status := get(record, "status")
	if exists && (status == "superseded" || status == "invalidated") {
		historical, e := HistoricalFingerprint(record)
		if e != nil {
			return e
		}
		if previousFingerprint == historical {
			fingerprint = previousFingerprint
		}
	}
	if fingerprint == "" {
		fingerprint, e = Fingerprint(record)
		if e != nil {
			return e
		}
	}
	var person any
	sc := get(record, "scope")
	if get(sc, "kind") == "scene" {
		person = get(sc, "personId")
	}
	raw, e = jsjson.Stringify(record)
	if e != nil {
		return e
	}
	if e = r.store.exec("INSERT INTO memories VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET scope_key=excluded.scope_key,person_id=excluded.person_id,content=excluded.content,fingerprint=excluded.fingerprint,value=excluded.value", id, scope(record), person, get(record, "content"), fingerprint, raw); e != nil {
		return e
	}
	if e = r.store.exec("DELETE FROM memory_sources WHERE memory_id=?", id); e != nil {
		return e
	}
	for _, source := range array(get(record, "sources")) {
		if e = r.store.exec("INSERT OR IGNORE INTO memory_sources VALUES(?,?,?,?)", id, get(source, "sceneId"), get(source, "messageId"), get(source, "senderId")); e != nil {
			return e
		}
	}
	if r.mutations.AfterWrite != nil {
		return r.mutations.AfterWrite(record)
	}
	return nil
}
