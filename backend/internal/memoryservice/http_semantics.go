package memoryservice

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
)

type SemanticsQuery struct {
	SceneID string
	Limit   int
}

type SemanticsAdminRead func(context.Context, *SemanticsQuery, func(int, []byte) error) (bool, error)

func (s *Service) BindSemanticsAdminRead(publisher *recovery.ReadPublisher, schemas *contract.Schemas) (SemanticsAdminRead, error) {
	opts := s.d.Authority
	now := opts.NowMillis
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
		opts.NowMillis = now
	}
	publish, err := s.d.Knowledge.Storage.BindAdminScope(publisher, s.d.Assertions, s.d.Commitments, s.d.Authority)
	if err != nil {
		return nil, err
	}
	useRows, err := s.d.Semantic.BindAdminCurrent(publisher)
	if err != nil {
		return nil, err
	}
	type scope struct {
		assertions  *assertions.Reader
		commitments *commitments.ReadService
		query       commitments.PublicationQuery
		knowledge   func(bool) *knowledge.Reader
	}
	type run func(*semantic.ModelReader) error
	withReader := func(cur scope, settings map[string]any, prepared map[any]bool, fn run) error {
		a := semantic.PrivacyAuthority{
			AssertReady: cur.commitments.AssertReady,
			Current: func(c semantic.Context, ref *Object) (*Object, error) {
				return semanticCurrent(cur.commitments, Context{SceneID: c.SceneID, Simulation: c.Simulation}, ref)
			},
			AnchorCurrent: func(c semantic.Context, ref *Object) (*Object, error) {
				v, err := cur.commitments.CurrentAssertion(commitments.Context{SceneID: c.SceneID, Simulation: c.Simulation}, semanticRef(ref))
				if err != nil || v == nil {
					return nil, err
				}
				return epObj("record", v.Record, "proofs", v.Proofs), nil
			},
		}
		a.PartitionKey = func(c semantic.Context, person any) (string, error) {
			if !prepared[person] {
				return "", errors.New("semantic_admin_partition_changed")
			}
			return semanticPartitionInReadScope(cur.assertions, opts, a.Current, c, person)
		}
		a.EpisodeAnchor = func(c semantic.Context, anchor *Object, parents []any, key string) (bool, error) {
			reader := cur.knowledge(settings["memoryKnowledgeEnabled"] == true)
			ports := episodePorts{query: reader.Query, current: func(c Context, ref *Object) (*Object, error) {
				return semanticCurrent(cur.commitments, c, ref)
			}, anchor: func(c Context, ref *Object) (*commitments.Assertion, error) {
				return cur.commitments.CurrentAssertion(commitments.Context{SceneID: c.SceneID, Simulation: c.Simulation}, semanticRef(ref))
			}}
			return ports.valid(Context{SceneID: c.SceneID, Simulation: c.Simulation}, anchor, parents, &key)
		}
		return useRows(cur.commitments.AssertReady, cur.query, semantic.ModelReadPolicy{
			Authority: a,
			Enabled:   func(semantic.Context) (bool, error) { return settings["memorySemanticEnabled"] == true, nil },
			NowMillis: func() float64 { return float64(now()) },
			ParseDate: func(value string) (float64, error) {
				ms, valid, err := opts.DateParser.ParseMillis(value)
				if !valid {
					return math.NaN(), err
				}
				return float64(ms), err
			},
		}, fn)
	}
	settings := func(r *assertions.Reader, scene string) (map[string]any, error) {
		real, err := realPeopleScene(r, scene)
		if err != nil || !real {
			return nil, err
		}
		raw, exists, err := r.SettingsInput()
		if err != nil {
			return nil, err
		}
		if !exists {
			return schemas.Parse("Settings", contract.DefaultSettingsInput())
		}
		return schemas.Parse("Settings", []byte(raw))
	}
	return func(ctx context.Context, q *SemanticsQuery, emit func(int, []byte) error) (bool, error) {
		var admitted bool
		prepared := map[any]bool{}
		if q != nil {
			var persons []any
			err := publish(ctx, func(r *assertions.Reader, c *commitments.ReadService, query commitments.PublicationQuery, k func(bool) *knowledge.Reader) ([]byte, error) {
				admitted = true
				flags, err := settings(r, q.SceneID)
				if err != nil || flags["memorySemanticEnabled"] != true {
					return nil, err
				}
				return nil, withReader(scope{r, c, query, k}, flags, prepared, func(read *semantic.ModelReader) (err error) {
					persons, err = read.SelectedAdminPartitions(semantic.Context{SceneID: q.SceneID}, q.Limit)
					return err
				})
			}, func([]byte) error { return nil })
			if err != nil {
				return admitted, err
			}
			if len(persons) != 0 {
				if err := s.prepareSemanticAdminPartitions(ctx, q.SceneID, persons, prepared, settings); err != nil {
					return true, err
				}
			}
		}
		code := http.StatusOK
		err := publish(ctx, func(r *assertions.Reader, c *commitments.ReadService, query commitments.PublicationQuery, k func(bool) *knowledge.Reader) ([]byte, error) {
			admitted = true
			if q == nil {
				code = http.StatusBadRequest
				return []byte(`{"error":"Invalid request"}`), nil
			}
			flags, err := settings(r, q.SceneID)
			if err != nil {
				return nil, err
			}
			if flags == nil {
				code = http.StatusNotFound
				return []byte(`{"error":"semantic_scene_unavailable"}`), nil
			}
			if flags["memorySemanticEnabled"] != true {
				return []byte(`{"enabled":false,"coverage":"complete","items":[],"unavailable":[]}`), nil
			}
			var page *Object
			if err := withReader(scope{r, c, query, k}, flags, prepared, func(read *semantic.ModelReader) (err error) {
				page, err = read.AdminList(semantic.Context{SceneID: q.SceneID}, q.Limit)
				return err
			}); err != nil {
				return nil, err
			}
			return projectSemanticAdmin(page, schemas)
		}, func(body []byte) error { return emit(code, body) })
		return admitted, err
	}, nil
}

func (s *Service) prepareSemanticAdminPartitions(ctx context.Context, scene string, persons []any, prepared map[any]bool, settings func(*assertions.Reader, string) (map[string]any, error)) error {
	return s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		var enabled bool
		if err := tx.Store.Read(ctx, func(r *assertions.Reader) error {
			flags, err := settings(r, scene)
			enabled = flags["memorySemanticEnabled"] == true
			return err
		}); err != nil || !enabled {
			return err
		}
		return tx.Store.WithWriteSession(func(session *recovery.WriteSession) error {
			a := s.semanticPrivacyAuthority(ctx, tx, session)
			for _, person := range persons {
				if _, err := a.PartitionKey(semantic.Context{SceneID: scene}, person); err != nil {
					return err
				}
				prepared[person] = true
			}
			return nil
		})
	})
}

func projectSemanticAdmin(page *Object, schemas *contract.Schemas) ([]byte, error) {
	items := []any{}
	for _, raw := range epArr(value(page, "items")) {
		v := object(raw)
		row := object(value(v, "candidate"))
		projected := semanticAdminFields(row, "id", "version", "personId", "kind", "text", "selectedParentIds",
			"status", "admission", "hypothesis", "anchorQualifications", "createdAt", "applicableUntil")
		if anchor := object(value(row, "episodeAnchor")); anchor != nil {
			_ = projected.Set("episodeAnchor", semanticAdminFields(anchor, "id", "version"))
		}
		_ = projected.Set("parents", semanticAdminList(row, "parents", func(parent *Object) *Object {
			return epObj("ref", semanticAdminRef(object(value(parent, "ref"))),
				"proofs", semanticAdminList(parent, "proofs", semanticAdminProof))
		}))
		_ = projected.Set("declarations", semanticAdminList(v, "declarations", func(declaration *Object) *Object {
			record := object(value(declaration, "record"))
			view := semanticAdminFields(record, "id", "content", "kind", "epistemicKind", "status", "origin")
			_ = view.Set("scope", semanticAdminFields(object(value(record, "scope")), "kind", "sceneId", "personId"))
			for _, key := range []string{"learnedAt", "observedAt", "occurredAt", "validFrom", "validUntil"} {
				if at, ok := record.Get(key); ok {
					_ = view.Set(key, at)
				}
			}
			_ = view.Set("sources", semanticAdminList(record, "sources", semanticAdminSource))
			if use, ok := record.Get("internalUse"); ok {
				_ = view.Set("internalUse", use)
			}
			return epObj("ref", semanticAdminRef(object(value(declaration, "ref"))), "record", view)
		}))
		_ = projected.Set("rootCount", float64(len(epArr(value(v, "roots")))))
		_ = projected.Set("applicability", value(v, "applicability"))
		_ = projected.Set("interpretation", value(v, "interpretation"))
		if err := schemas.ValidateSemanticAdministrationItem(projected); err != nil {
			return nil, err
		}
		items = append(items, projected)
	}
	return jsjson.Marshal(epObj("enabled", true, "coverage", value(page, "coverage"), "items", items, "unavailable", value(page, "unavailable")))
}

func semanticAdminFields(in *Object, keys ...string) *Object {
	out := new(Object)
	if in != nil {
		for _, key := range keys {
			if v, ok := in.Get(key); ok {
				_ = out.Set(key, v)
			}
		}
	}
	return out
}

func semanticAdminList(in *Object, key string, project func(*Object) *Object) []any {
	rows := epArr(value(in, key))
	out := make([]any, len(rows))
	for i, row := range rows {
		out[i] = project(object(row))
	}
	return out
}

func semanticAdminRef(in *Object) *Object {
	return semanticAdminFields(in, "id", "version", "deletionEpoch")
}

func semanticAdminSource(in *Object) *Object {
	return semanticAdminFields(in, "sceneId", "messageId", "senderId")
}

func semanticAdminProof(proof *Object) *Object {
	view := epObj("sources", semanticAdminList(proof, "sources", semanticAdminSource), "parents", value(proof, "parents"))
	if _, ok := proof.Get("parentReferences"); ok {
		_ = view.Set("parentReferences", semanticAdminList(proof, "parentReferences", semanticAdminRef))
	}
	if witness := object(value(proof, "subjectWitness")); witness != nil {
		_ = view.Set("subjectWitness", semanticAdminFields(witness, "kind", "personId", "sourceMessageId", "mentionPartIndex"))
	}
	if _, ok := proof.Get("sourceObservations"); ok {
		_ = view.Set("sourceObservations", semanticAdminList(proof, "sourceObservations", func(source *Object) *Object {
			return semanticAdminFields(source, "sceneId", "messageId", "senderId", "sentAt", "receivedAt")
		}))
	}
	return view
}
