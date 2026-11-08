package experienceservice

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// NativePorts implements engine.ts's persisted configuration and source proof.
// Preview must return a live runtime snapshot while honoring the supplied writer
// lease; history/tool output is not an admission substitute. Compare comes from
// topiccollation.Collator.Compare for the deployment's pinned resolved locale.
func NativePorts(preview func(*recovery.WriteSession, string, string) (*jsjson.Object, error), compare func(string, string) (int, error)) (Ports, error) {
	if preview == nil || compare == nil {
		return Ports{}, errors.New("experience_ports_required")
	}
	value := func(ws *recovery.WriteSession, q string, a ...any) (out *jsjson.Object, err error) {
		err = guarded(func() {
			s := store{ws}
			rows := s.values(q, a...)
			if len(rows) == 0 {
				return
			}
			v := take(jsjson.Parse(rows[0]))
			if v == nil {
				return
			}
			var ok bool
			out, ok = v.(*jsjson.Object)
			if !ok {
				fail("experience_stored_value_invalid")
			}
		})
		return
	}
	scene := func(ws *recovery.WriteSession, id string) (*jsjson.Object, error) {
		return value(ws, "SELECT value FROM scenes WHERE id=?", id)
	}
	prop := func(o *jsjson.Object, k string) any {
		if o == nil {
			return nil
		}
		v, _ := o.Get(k)
		return v
	}
	revoked := func(ws *recovery.WriteSession, scene, id string) (yes bool, err error) {
		err = guarded(func() {
			yes = (store{ws}).exists("SELECT 1 FROM recall_tombstones WHERE scene_id=? AND message_id=?", scene, id)
		})
		return
	}
	p := Ports{Compare: compare, Validate: func(p Policy) (Receipt, error) { return ValidateExperiencePolicy(p), nil }, SourceRevoked: revoked}
	p.Settings = func(ws *recovery.WriteSession) (f Settings, err error) {
		err = guarded(func() {
			o := take(value(ws, "SELECT value FROM config WHERE key='settings'"))
			f.Enabled = prop(o, "memoryExperienceEnabled") == true
			f.AutoActivate = prop(o, "memoryExperienceAutoActivateEnabled") == true
			f.Hints = prop(o, "memoryExperienceHintsEnabled") == true
			f.FeedbackTrials = prop(o, "memoryExperienceFeedbackEnabled") == true
			r := (store{ws}).values("SELECT value FROM config WHERE key='memory-experience-feedback-revision'")
			if len(r) == 0 {
				f.Revision = ptr(int64(0))
				return
			}
			v := take(jsjson.Parse(r[0]))
			if n, ok := v.(float64); ok && n >= 0 && n <= 9007199254740991 && float64(int64(n)) == n {
				f.Revision = ptr(int64(n))
			}
		})
		return
	}
	p.SceneAvailable = func(ws *recovery.WriteSession, id string) (bool, error) {
		o, e := scene(ws, id)
		k := prop(o, "kind")
		return k == "group" || k == "private", e
	}
	p.LearningEnabled = func(ws *recovery.WriteSession, id string) (bool, error) {
		o, e := scene(ws, id)
		return prop(o, "learningEnabled") == true, e
	}
	p.SourceCurrent = func(ws *recovery.WriteSession, sc, id string, sender *string) (valid bool, err error) {
		err = guarded(func() {
			if take(revoked(ws, sc, id)) {
				return
			}
			v := take(sourceevents.BorrowReadScope(ws.AssertReady, func(q string, a ...any) (sourceevents.Rows, error) { return ws.Query(q, a...) }))
			head := take(v.Registered(sc, id))
			if head == nil {
				valid = true
				return
			}
			o := take(scene(ws, sc))
			if o == nil {
				return
			}
			author := head.Identity.SenderID
			if sender != nil {
				author = *sender
			}
			if prop(o, "adapterId") != head.Identity.AdapterID || prop(o, "accountId") != head.Identity.AccountID || author != head.Identity.SenderID {
				return
			}
			valid = !take(v.HasEdits(head.Identity))
		})
		return
	}
	p.AdmissionSourceAvailable = func(ws *recovery.WriteSession, sc, id, sender string) (valid bool, err error) {
		err = guarded(func() {
			m := take(preview(ws, sc, id))
			valid = m != nil && prop(m, "senderId") == sender && sender != "bot" && prop(m, "origin") == "live" && prop(m, "status") == "available" && !take(revoked(ws, sc, id))
		})
		return
	}
	p.TargetAvailable = func(ws *recovery.WriteSession, sc, id string) (valid bool, err error) {
		err = guarded(func() {
			m := take(preview(ws, sc, id))
			valid = m != nil && prop(m, "senderId") == "bot" && prop(m, "origin") == "live" && prop(m, "status") == "available" && !take(revoked(ws, sc, id))
		})
		return
	}
	return p, nil
}
