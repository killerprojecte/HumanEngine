package memoryservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionproposal"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
)

// proposeAssertion is only the nested backing writer. The future outer typed
// proposal command must prepare declarations and admit graphs before returning.
func (s *Service) proposeAssertion(ctx context.Context, tx *assertions.RevisionTxn, input, candidate *Object, runtime assertionproposal.Runtime) (assertionproposal.Result, error) {
	command, e := assertionproposal.New(s.owners(), s.d.Authority, runtime)
	if e != nil {
		return assertionproposal.Result{}, e
	}
	return command.ProposeAssertion(ctx, tx, input, candidate)
}
