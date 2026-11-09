package candidateservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// NativeExperienceOptions constructs the caller-owned runtime against the exact
// native authority created inside this candidate. BuildRuntime must only compose
// the runtime, not start ingress/model work before the constructor returns.
// The returned runtime must use this exact Authority capability. Caller retains
// runtime lifecycle ownership after success. AbortRuntime is required and is
// called only when construction fails after BuildRuntime returned a runtime.
// Stop runtime ingress before closing a successful candidate; Close never closes it.
type NativeExperienceOptions struct {
	BuildRuntime func(*sourceevents.RuntimeAuthority) (*botruntime.Runtime, error)
	Compare      func(string, string) (int, error)
	AbortRuntime func(*botruntime.Runtime)
}

// OpenWithExperience explicitly initializes the native owner and enables all
// Experience administration routes. Open remains the readonly preview. This is
// not installation enrollment, reconciliation, or a claim of production boot.
func OpenWithExperience(ctx context.Context, database, token string, logs observability.RetainedLogger, options NativeExperienceOptions) (*Service, error) {
	return open(ctx, database, token, logs, false, &options)
}

// OpenWithMemoryAdministration composes Habit and Experience against the same
// owned database/writer. Close drains active calls before closing either owner.
func OpenWithMemoryAdministration(ctx context.Context, database, token string, logs observability.RetainedLogger, options NativeExperienceOptions) (*Service, error) {
	return open(ctx, database, token, logs, true, &options)
}
