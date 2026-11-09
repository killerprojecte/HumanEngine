package modelread

import "context"

// Capture matches source's optional ticket capture. Disabled access returns no
// ticket; callers such as Engine planner context still perform their later
// Assert fence. Require remains the explicit fail-before-read entry point.
func (p *Policy) Capture(ctx context.Context, scene string) (*Ticket, error) {
	enabled, revision, personal, err := p.state(ctx, scene)
	if err != nil || !enabled {
		return nil, err
	}
	ticket := &Ticket{owner: p, scene: scene, revision: revision, personalization: personal}
	ticket.self = ticket
	return ticket, nil
}
