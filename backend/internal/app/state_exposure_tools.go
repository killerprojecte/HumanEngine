package app

import (
	"context"
	_ "embed"
	"errors"
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func (a *Application) composeStateMemory(o Owners) error {
	owners, err := a.newStateExposureOwners(o)
	if err != nil {
		return err
	}
	a.StateExposures = owners
	// Guard order is current/history/timeline/historical before Identity.
	a.Engine.Runtime.AddExtension(owners.compose(a, botruntime.Extension{}))
	return nil
}
func (a *Application) registerStateMemoryTools(memory *memoryOwners) {
	owners := a.StateExposures
	// Advertisement order follows Identity, unlike the capture guard order.
	base := botruntime.Extension{ToolsForTurn: func(*botruntime.TurnContext) []*jsjson.Object { return enabledStateExposureSchemas(memory.snapshot) }, Execute: func(ctx context.Context, name string, input any, c *botruntime.TurnContext, done func(any, error)) {
		value, err := owners.execute(a, ctx, name, input, c)
		done(value, err)
	}}
	ext := owners.compose(a, base)
	ext.CaptureToolResultGuards = nil // The preceding guard-only extension owns capture once.
	a.Engine.Runtime.AddExtension(ext)
}

func (o *stateExposureOwners) execute(a *Application, ctx context.Context, name string, input any, c *botruntime.TurnContext) (any, error) {
	if !stateExposureTool(name) {
		return nil, errors.New("runtime_unknown_tool")
	}
	if err := a.gateStateExposure(ctx, name, c); err != nil {
		return nil, err
	}
	args, err := stateExposureArguments(name, input)
	if err != nil {
		return nil, err
	}
	var values []*jsjson.Object
	var deliver func(context.Context, *botruntime.TurnContext, []any) ([]*jsjson.Object, error)
	switch name {
	case "memory.commitments":
		values, err = o.current.Query(ctx, c, args)
		deliver = o.current.Deliver
	case "memory.history":
		id, _ := args.Get("memoryId")
		anchor := a.historicalAnchor(id.(string), c)
		if anchor == nil {
			return nil, errors.New("memory_history_anchor_not_exposed")
		}
		_ = args.Set("anchor", anchor)
		values, err = o.historical.Query(ctx, c, args)
		deliver = o.historical.Deliver
	case "memory.commitmentHistory", "memory.commitmentTimeline":
		id, _ := args.Get("id")
		version, _ := args.Get("version")
		epoch, _ := args.Get("deletionEpoch")
		ref := commitments.Reference{ID: id.(string), Version: version.(float64), DeletionEpoch: epoch.(float64)}
		if name == "memory.commitmentHistory" {
			values, err = o.history.Query(ctx, c, ref)
			deliver = o.history.Deliver
		} else {
			options := new(jsjson.Object)
			for _, key := range []string{"asOfRecorded", "limit"} {
				if value, ok := args.Get(key); ok {
					_ = options.Set(key, value)
				}
			}
			values, err = o.timeline.Query(ctx, c, ref, options)
			deliver = o.timeline.Deliver
		}
	}
	if err != nil {
		return nil, err
	}
	if c.MemoryExposureDeferred() {
		return memoryToolArray(values), nil
	}
	values, err = deliver(ctx, c, memoryToolArray(values))
	return memoryToolArray(values), err
}

// Zod's default ISO datetime accepts UTC Z only, mandatory seconds, optional
// arbitrary precision fraction and valid Gregorian dates including year 0000.
// Do not substitute RFC3339 (which accepts offsets), or JS Date parsing.
var stateISO = regexp.MustCompile(`^([0-9]{4})-([0-9]{2})-([0-9]{2})T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](\.[0-9]+)?Z$`)

func stateISODate(value string) bool {
	parts := stateISO.FindStringSubmatch(value)
	if parts == nil {
		return false
	}
	year, _ := strconv.Atoi(parts[1])
	month, _ := strconv.Atoi(parts[2])
	day, _ := strconv.Atoi(parts[3])
	if month < 1 || month > 12 || day < 1 {
		return false
	}
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return date.Year() == year && int(date.Month()) == month && date.Day() == day
}
func stateExposureArguments(name string, input any) (*jsjson.Object, error) {
	args, ok := input.(*jsjson.Object)
	invalid := func() (*jsjson.Object, error) { return nil, errors.New("state_memory_tool_arguments_invalid") }
	if !ok {
		return invalid()
	}
	out := new(jsjson.Object)
	if name == "memory.commitments" {
		if value, _ := args.Get("personId"); value != nil {
			if person, ok := value.(string); ok {
				_ = out.Set("personId", person)
			}
		}
		if value, ok := args.Get("lifecycle"); ok && !jsjson.IsUndefined(value) {
			if value != "open" && value != "terminal" && value != "all" {
				return invalid()
			}
			_ = out.Set("lifecycle", value)
		}
		return out, nil
	}
	allowed := map[string]bool{"id": true, "version": true, "deletionEpoch": true}
	required := []string{"id", "version", "deletionEpoch"}
	if name == "memory.history" {
		allowed = map[string]bool{"memoryId": true, "asOfRecorded": true, "validAt": true, "limit": true}
		required = []string{"memoryId"}
	}
	if name == "memory.commitmentTimeline" {
		allowed["asOfRecorded"] = true
		allowed["limit"] = true
	}
	for _, key := range required {
		if value, ok := args.Get(key); !ok || jsjson.IsUndefined(value) {
			return invalid()
		}
	}
	for _, entry := range args.Members() {
		if !allowed[entry.Key] {
			return invalid()
		}
		value := entry.Value
		if jsjson.IsUndefined(value) {
			continue
		}
		switch entry.Key {
		case "id", "memoryId", "asOfRecorded", "validAt":
			str, ok := value.(string)
			if !ok || (entry.Key == "id" || entry.Key == "memoryId") && str == "" {
				return invalid()
			}
			if name == "memory.commitmentTimeline" && entry.Key == "asOfRecorded" && !stateISODate(str) {
				return invalid()
			}
		default:
			number, ok := value.(float64)
			if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) || math.Abs(number) > 9007199254740991 {
				return invalid()
			}
			minimum := float64(1)
			if entry.Key == "deletionEpoch" {
				minimum = 0
			}
			if number < minimum || entry.Key == "limit" && number > 20 {
				return invalid()
			}
		}
		_ = out.Set(entry.Key, value)
	}
	return out, nil
}

//go:embed testdata/state-tools-source.json
var stateToolsSource string

func stateExposureSchemas() []*jsjson.Object {
	value, err := jsjson.Parse(stateToolsSource)
	if err != nil {
		panic(err)
	}
	raw := value.([]any)
	tools := make([]*jsjson.Object, len(raw))
	for i, v := range raw {
		tools[i] = v.(*jsjson.Object)
	}
	return tools
}
func enabledStateExposureSchemas(snapshot *settingsSnapshot) []*jsjson.Object {
	// Load the one published source settings snapshot once per getter invocation.
	settings := snapshot.value.Load().(map[string]any)
	current := settings["memoryCommitmentsEnabled"] == true && settings["memoryCommitmentsAnswerEnabled"] == true
	historical := settings["memoryHistoricalReadEnabled"] == true
	out := []*jsjson.Object{}
	for _, tool := range stateExposureSchemas() {
		name, _ := tool.Get("name")
		enabled := current
		switch name {
		case "memory.commitmentTimeline", "memory.commitmentHistory":
			enabled = current && historical
		case "memory.history":
			enabled = historical
		}
		if enabled {
			out = append(out, tool)
		}
	}
	return out
}
