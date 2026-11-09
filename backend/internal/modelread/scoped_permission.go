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
		enabled, _, personal, e := sceneState(scene)
		allowed = e == nil && enabled && personal
		return e
	})
	return
}
