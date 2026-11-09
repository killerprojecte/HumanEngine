package toolresults

import "context"

// Generation is an optional native publication capability for one plugin load.
// The owner supplies that load's original lifecycle, not a derived context whose
// AfterFunc cancellation may still be queued. Cancelling that lifecycle revokes
// admission and publication synchronously. The cache never stores a permanent
// plugin-ID tombstone, so a new generation can cache normally after re-enable.
// It does not own cancellation or replace source guards and scope checks.
type Generation struct{ life context.Context }

func NewGeneration(life context.Context) *Generation { return &Generation{life: life} }
func (g *Generation) current() bool                  { return g == nil || g.life != nil && g.life.Err() == nil }
func (o Owner) generationCurrent() bool              { return o.Generation.current() }
