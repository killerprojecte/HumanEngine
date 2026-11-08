package botruntime

import "context"

type acceptedSourceEdit struct {
	status          string
	current, result *Object
}

// EditSceneResult is the transport root for an explicit source edit. Persisted
// source authority can outlive an idle runtime scene. An unorderable edit does
// not create a runtime state; an accepted revision is never observed twice.
func (r *Runtime) EditSceneResult(ctx context.Context, scene Scene, message, edit *Object, schedule bool) (*Object, error) {
	done, admitted := r.beginOperation()
	if !admitted {
		return nil, code("runtime_closed")
	}
	defer done()
	if r.state(scene.ID) != nil {
		var result *Object
		_, err := r.editResult(ctx, scene.ID, message, edit, schedule, func(value *Object) { result = value }, nil)
		return result, err
	}
	detailed, ok := r.services.Authority.(DetailedSourceAuthority)
	if !ok {
		return nil, code("runtime_detailed_source_authority_required")
	}
	status, current, result, err := detailed.ObserveDetailed(ctx, scene, cloneJSON(message).(*Object), edit)
	if err != nil || status != "edited" {
		return result, err
	}
	if current == nil {
		return nil, code("source_authority_missing_snapshot")
	}
	if err = r.RegisterScene(scene); err != nil {
		return nil, err
	}
	var final *Object
	_, err = r.editResult(ctx, scene.ID, message, edit, schedule, func(value *Object) { final = value }, &acceptedSourceEdit{status, current, result})
	return final, err
}
