package experienceservice

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/profileidentity"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// OwnedBy rejects borrowed scopes and services from another writer/database.
func (s *Service) OwnedBy(main *sql.DB, authority *recovery.Authority) bool {
	return s != nil && s.main == main && s.authority == authority && s.session == nil && s.bindingError == nil
}

type HTTPRequest struct {
	Operation, SceneID, ID, Status string
	ExpectedVersion                int64
	Options                        ListOptions
}

func httpFailure(err error) (int, []byte, bool) {
	if err == nil {
		return 200, []byte(`{"ok":true}`), true
	}
	switch err.Error() {
	case "experience_unavailable":
		return 404, []byte(`{"error":"Experience unavailable"}`), true
	case "experience_version_conflict", "experience_validation_ineligible", "experience_validation_failed", "experience_activation_ineligible", "experience_invalid_transition":
		return 409, []byte(`{"error":"Experience changed or is not eligible; refresh before reviewing"}`), true
	}
	return 0, nil, false
}

// PublishHTTP commits a mutation once, then rechecks its exact witness before
// immutable response acceptance. emit must enqueue owned bytes, never do I/O.
// A failure after COMMIT is an unknown response outcome, never a retry signal.
func (s *Service) PublishHTTP(ctx context.Context, request HTTPRequest, emit func(int, []byte) error) error {
	if !s.OwnedBy(s.main, s.authority) || emit == nil {
		return errors.New("experience_publication_requires_root")
	}
	var witness *recovery.ReadWitness
	status := 200
	var body []byte
	var projectionError error
	err := s.TransactionWithSession(ctx, func(bound *Service, ws *recovery.WriteSession) error {
		var e error
		witness, e = ws.CaptureReadWitness(func() error {
			return guarded(func() {
				scene := take(ws.Query("SELECT value FROM scenes WHERE id=?", take(jsjson.UTF8Text(request.SceneID))))
				real := false
				if scene.Next() {
					var raw string
					check(scene.Scan(&raw))
					o, ok := take(jsjson.Parse(raw)).(*jsjson.Object)
					if ok {
						k, _ := o.Get("kind")
						real = k == "group" || k == "private"
					}
				}
				check(scene.Err())
				check(scene.Close())
				if !real {
					status = 404
					body = []byte(`{"error":"Scene unavailable"}`)
				} else {
					_ = take(httpSettings(ws))
					scope := Context{SceneID: request.SceneID}
					var operationError error
					switch request.Operation {
					case "list":
						result := take(bound.AdminList(ctx, scope, request.Options))
						body, projectionError = bound.httpView(ws, request.SceneID, result)
					case "validate":
						_, operationError = bound.Validate(ctx, scope, request.ID, request.ExpectedVersion)
					case "review":
						_, operationError = bound.Review(ctx, scope, request.ID, request.ExpectedVersion, request.Status, "administrator_review")
					case "delete":
						operationError = bound.Delete(ctx, scope, request.ID, request.ExpectedVersion)
					default:
						fail("experience_http_operation_invalid")
					}
					if request.Operation != "list" {
						var handled bool
						status, body, handled = httpFailure(operationError)
						if !handled {
							check(operationError)
						}
					}
				}
			})
		})
		return e
	})
	if err != nil {
		return err
	}
	return s.authority.Publish(ctx, s.main, func(c *sql.Conn) ([]byte, error) {
		if e := witness.Verify(ctx, c); e != nil {
			return nil, e
		}
		if projectionError != nil {
			return nil, projectionError
		}
		return body, nil
	}, func(b []byte) error { return emit(status, b) })
}

func (s *Service) httpView(ws *recovery.WriteSession, scene string, result AdminResult) (body []byte, err error) {
	err = guarded(func() {
		name := func(person string) (string, bool) {
			rows := (store{ws}).values("SELECT value FROM config WHERE key=?", "identity:"+scene+":"+person)
			if len(rows) == 0 {
				return "", false
			}
			v := take(jsjson.Parse(rows[0]))
			if v == nil {
				fail("experience_identity_null")
			}
			o, ok := v.(*jsjson.Object)
			if !ok {
				return "", false
			}
			v, _ = o.Get("senderDisplayName")
			n, yes, e := profileidentity.DisplayName(v)
			check(e)
			return n, yes
		}
		named := func(o *jsjson.Object, key, person string) {
			if n, ok := name(person); ok {
				check(o.Set(key, n))
			}
		}
		items := []any{}
		for _, row := range result.Items {
			material := row.presentation
			if material == nil {
				material = captureAdminPresentation(row)
			}
			o := cloneReferenceValue(material.policy).(*jsjson.Object)
			check(o.Set("trialEligible", row.TrialEligible))
			check(o.Set("expired", row.Expired))
			if person, present := row.Condition.value().Get("personId"); present {
				named(o, "personName", take(profileidentity.StringValue(person)))
			}

			if t := row.Trial; t != nil {
				trial := object("version", float64(t.Version), "policyVersion", float64(t.PolicyVersion), "evidencePolicyVersion", float64(t.EvidencePolicyVersion), "phase", t.Phase, "createdAt", t.CreatedAt, "expiresAt", t.ExpiresAt, "positiveCount", float64(len(t.Positives)), "instruction", object("id", t.Instruction.ID, "version", float64(t.Instruction.Version)))
				named(trial, "requesterName", t.RequesterID)
				if t.Reason != "" {
					check(trial.Set("reason", t.Reason))
				}
				check(o.Set("trial", trial))
			}
			sources := []any{}
			for i, source := range row.Sources {
				rawSource := material.sources[i]
				v := keepPresentation(rawSource, "observedAt", "code")
				if original, ok := getValue(rawSource, "source").(*jsjson.Object); ok {
					for _, key := range []string{"messageId", "senderId"} {
						if value, yes := original.Get(key); yes {
							check(v.Set(key, cloneReferenceValue(value)))
						}
					}
				}
				named(v, "senderName", source.Source.SenderID)
				sources = append(sources, v)
			}
			check(o.Set("sources", sources))
			if material.validationPresent {
				check(o.Set("validation", cloneReferenceValue(material.validation)))
			}

			check(o.Set("uses", object("exposed", float64(row.Uses["exposed"]), "applied", float64(row.Uses["applied"]), "declined", float64(row.Uses["declined"]))))
			check(validateHTTPView(o))
			items = append(items, o)
		}
		settings := take(httpSettings(ws))
		body = take(jsjson.Marshal(object("enabled", settings["memoryExperienceEnabled"], "sceneId", scene, "items", items, "coverage", result.Coverage)))
	})
	return
}

var httpSchemasOnce sync.Once
var httpSchemas *contract.Schemas
var httpSchemasError error

func httpSettings(ws *recovery.WriteSession) (settings map[string]any, err error) {
	httpSchemasOnce.Do(func() { httpSchemas, httpSchemasError = contract.NewSchemas() })
	if httpSchemasError != nil {
		return nil, httpSchemasError
	}
	err = guarded(func() {
		rows := (store{ws}).values("SELECT value FROM config WHERE key='settings'")
		raw := contract.DefaultSettingsInput()
		if len(rows) > 0 {
			raw = []byte(rows[0])
		}
		settings = take(httpSchemas.Parse("Settings", raw))
	})
	return
}
