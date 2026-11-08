package relations

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/relationstorage"
)

// AdmitInAssertionScope reuses the active outer proposal owner; it neither
// acquires another writer nor replaces exact backing references with previews.
func AdmitInAssertionScope(ctx context.Context, storage *relationstorage.Store, active *assertions.Store, options Options, input Context, backing *Object, proposals []any) (out Admission, err error) {
	if storage == nil || active == nil {
		return out, errors.New("relation_admission_owner_required")
	}
	if options.Enabled == nil || options.LearningEnabled == nil || options.AutoAdmission == nil || options.Authority.DateParser == nil || options.Authority.Compare == nil {
		return out, errors.New("relation_dependencies_required")
	}
	err = active.WithWriteSession(func(session *recovery.WriteSession) error {
		bound, e := storage.BindAdmissionSession(session)
		if e != nil {
			return e
		}
		service := &Service{Storage: bound, assertions: active, options: options}
		out, e = service.Admit(ctx, input, backing, proposals)
		return e
	})
	return
}
