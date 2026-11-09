package app

import (
	"context"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
)

// stateExposureOwners is a second-batch composition candidate. It is not
// registered by the first application release. The second batch registers tools.
// Current/history/timeline must share this exact port instance.
type stateExposureOwners struct {
	current    *memoryservice.CommitmentExposure
	history    *memoryservice.CommitmentHistoryExposure
	timeline   *memoryservice.CommitmentTimelineExposure
	historical *memoryservice.HistoricalExposure
}

func (a *Application) newStateExposureOwners(o Owners) (*stateExposureOwners, error) {
	port, err := memoryservice.NewExposurePort(a.Semantic.Memory, o.Sources, o.Main, o.Authority)
	if err != nil {
		return nil, err
	}
	owners := new(stateExposureOwners)
	owners.current, err = memoryservice.NewCommitmentExposure(port)
	if err != nil {
		return nil, err
	}
	owners.history, err = memoryservice.NewCommitmentHistoryExposure(port, owners.current)
	if err != nil {
		return nil, err
	}
	owners.timeline, err = memoryservice.NewCommitmentTimelineExposure(port, owners.current)
	if err != nil {
		return nil, err
	}
	owners.historical, err = memoryservice.NewHistoricalExposure(port)
	if err != nil {
		return nil, err
	}
	return owners, nil
}

func stateExposureTool(name string) bool {
	switch name {
	case "memory.commitments", "memory.commitmentHistory", "memory.commitmentTimeline", "memory.history":
		return true
	default:
		return false
	}
}

// gateStateExposure is called before query AND before actual selected delivery.
// Source memory.history permits non-personal reads, unlike commitment tools.
func (a *Application) gateStateExposure(ctx context.Context, name string, c *botruntime.TurnContext) error {
	if name == "memory.history" {
		if c.Mode != "real" {
			return nil
		}
		_, err := a.Engine.TurnRead.Begin(ctx, c)
		return err
	}
	return a.Engine.TurnRead.GateTool(ctx, c)
}

// compose adds guards in source order. Register the returned extension after
// model-memory and before identity. It does not repeat the model-read capture
// already registered by Meme. The public extension supplies Execute and schemas.
func (o *stateExposureOwners) compose(a *Application, base botruntime.Extension) *botruntime.Extension {
	ext := o.current.ComposeExtension(base)
	ext = o.history.ComposeExtension(*ext)
	ext = o.timeline.ComposeExtension(*ext)
	ext = o.historical.ComposeExtension(*ext)
	delivered := ext.Delivered
	ext.Delivered = func(name string, value any, c *botruntime.TurnContext, input any) (any, error) {
		if stateExposureTool(name) {
			if err := a.gateStateExposure(context.Background(), name, c); err != nil {
				return nil, err
			}
		}
		return delivered(name, value, c, input)
	}
	return ext
}

// historicalAnchor only uses references actually committed to the model. An
// object returned by Prepare is not an anchor, even if its fields look equal.
func (a *Application) historicalAnchor(memoryID string, c *botruntime.TurnContext) *jsjson.Object {
	for _, ref := range a.MemoryExposure.ReadExposed(c) {
		if ref.ID == memoryID {
			return memoryToolObject("id", ref.ID, "version", float64(ref.Version), "deletionEpoch", float64(ref.DeletionEpoch))
		}
	}
	return nil
}
