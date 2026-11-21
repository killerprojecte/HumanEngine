package modelread

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// PersonalizationAllowedScoped reuses the caller's publication/write child.
// This checks current permission; exact ticket-revision guards remain separate.
func (p *RuntimePolicy) PersonalizationAllowedScoped(reads botruntime.ReadAuthority, sceneID string) (allowed bool, err error) {
	return p.permissionAllowedScoped(reads, sceneID, true)
}

// AllowedScoped checks the current non-personal model-read permission using the
// exact supplied read capability, including uncommitted writer state.
func (p *RuntimePolicy) AllowedScoped(reads botruntime.ReadAuthority, sceneID string) (bool, error) {
	return p.permissionAllowedScoped(reads, sceneID, false)
}

func (p *RuntimePolicy) permissionAllowedScoped(reads botruntime.ReadAuthority, sceneID string, requirePersonal bool) (allowed bool, err error) {
	enabled, _, personal, err := p.sceneStateScoped(reads, sceneID)
	return enabled && (!requirePersonal || personal), err
}

// CapturePersonalizationScoped pins the exact scene policy in the caller's
// already-held source publication. No independent snapshot is opened.
func (p *RuntimePolicy) CapturePersonalizationScoped(reads botruntime.ReadAuthority, sceneID string) (*Ticket, error) {
	enabled, revision, personal, err := p.sceneStateScoped(reads, sceneID)
	if err != nil || !enabled || !personal {
		return nil, err
	}
	ticket := &Ticket{owner: p.Policy, scene: sceneID, revision: revision, personalization: true}
	ticket.self = ticket
	return ticket, nil
}

func (p *RuntimePolicy) ValidatePersonalizationScoped(reads botruntime.ReadAuthority, ticket *Ticket) (bool, error) {
	return p.validScoped(reads, ticket)
}

func (p *RuntimePolicy) sceneStateScoped(reads botruntime.ReadAuthority, sceneID string) (enabled bool, revision float64, personal bool, err error) {
	err = p.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
		if e := scope.CheckBinding(p.main, p.authority); e != nil {
			return e
		}
		rows, e := scope.Query("SELECT value FROM scenes WHERE id=?", sceneID)
		if e != nil {
			return e
		}
		defer rows.Close()
		var scene *assertions.Object
		if rows.Next() {
			var raw string
			if e = rows.Scan(&raw); e != nil {
				return e
			}
			v, e := jsjson.Parse(raw)
			if e != nil {
				return e
			}
			scene, _ = v.(*assertions.Object)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		enabled, revision, personal, e = sceneState(scene)
		return e
	})
	return
}
