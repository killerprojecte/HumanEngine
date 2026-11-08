package learningruntime

import "github.com/killerprojecte/HumanEngine/backend/internal/learningbatch"

func modelReason(err error) string { return learningbatch.ModelFailureReason(err) }
