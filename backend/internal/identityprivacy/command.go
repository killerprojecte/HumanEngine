package identityprivacy

import (
	"errors"
	"sort"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

var (
	ErrRejected          = errors.New("identity_change_rejected_refresh_required")
	ErrAliasUnavailable  = errors.New("identity_alias_unavailable")
	ErrNamespaceMismatch = errors.New("identity_namespace_mismatch")
)

type Command struct {
	transition
	sceneID string
}

func (c *Command) SceneID() string { return c.sceneID }

func ParseCommand(value any) *Command {
	raw, ok := value.(*jsjson.Object)
	if !ok {
		return nil
	}
	keys := []string{"action", "operationId", "expectedRevision", "confirmed"}
	kind, _ := get(raw, "action").(string)
	switch kind {
	case "link", "unlink":
		keys = append(keys, "left", "right")
	case "split":
		keys = append(keys, "partition")
	case "alias_put":
		keys = append(keys, "aliasId", "sceneId", "endpoint", "label", "validFrom", "validUntil")
	case "alias_remove":
		keys = append(keys, "aliasId", "sceneId")
	default:
		return nil
	}
	o, ok := strict(raw, keys...)
	if !ok || get(o, "confirmed") != true || !bounded(get(o, "operationId"), 160) || !integer(get(o, "expectedRevision")) {
		return nil
	}
	c := Command{transition: transition{kind: kind, operationID: get(o, "operationId").(string), expectedRevision: get(o, "expectedRevision").(float64)}}
	switch kind {
	case "link", "unlink", "split":
		points := []any{get(o, "left"), get(o, "right")}
		if kind == "split" {
			partition, ok := get(o, "partition").([]any)
			if !ok || len(partition) == 0 || len(partition) > 32 {
				return nil
			}
			points = partition
		}
		for i, v := range points {
			ep, ok := endpoint(v)
			if !ok {
				return nil
			}
			points[i] = ep
		}
		sort.SliceStable(points, func(i, j int) bool { return endpointLess(points[i].(*jsjson.Object), points[j].(*jsjson.Object)) })
		c.payload = points
		c.targets.Endpoints = make([]recovery.IdentityEndpoint, len(points))
		for i, p := range points {
			c.targets.Endpoints[i] = targetEndpoint(p.(*jsjson.Object))
		}
	case "alias_put", "alias_remove":
		if !bounded(get(o, "aliasId"), 160) || !bounded(get(o, "sceneId"), 160) {
			return nil
		}
		id := get(o, "aliasId").(string)
		c.sceneID = get(o, "sceneId").(string)
		c.targets.AliasIDs = []string{id}
		if kind == "alias_remove" {
			c.payload = object("aliasId", id)
			break
		}
		ep, ok := endpoint(get(o, "endpoint"))
		if !ok || !bounded(get(o, "label"), 200) || !instant(get(o, "validFrom")) || !instant(get(o, "validUntil")) {
			return nil
		}
		c.payload = object("id", id, "endpoint", ep, "label", get(o, "label"), "scopeSceneId", c.sceneID,
			"source", object("kind", "manual", "administratorId", "authenticated-admin"), "validFrom", get(o, "validFrom"), "validUntil", get(o, "validUntil"))
		c.targets.Endpoints = []recovery.IdentityEndpoint{targetEndpoint(ep)}
	}
	return &c
}

func targetEndpoint(ep *jsjson.Object) recovery.IdentityEndpoint {
	return recovery.IdentityEndpoint{AdapterID: get(ep, "adapterId").(string), AccountID: get(ep, "accountId").(string), PersonID: get(ep, "personId").(string)}
}

// RunAdminCommand receives the real scene namespace from the same serialized
// root transaction. The registry and operation CAS are read once in that scope.
func RunAdminCommand(session *recovery.WriteSession, c *Command, adapterID, accountID string) (Acknowledgement, error) {
	var ack Acknowledgement
	if c.kind == "alias_put" {
		ep := c.targets.Endpoints[0]
		if ep.AdapterID != adapterID || ep.AccountID != accountID {
			return ack, ErrNamespaceMismatch
		}
		// A well-formed date outside its alias interval is a transition conflict.
		if _, ok := alias(c.payload); !ok {
			return ack, ErrRejected
		}
	}
	state, exists, err := loadSessionRegistry(session)
	if err != nil {
		if c.kind == "alias_remove" {
			// The source's scene-ownership lookup occurs outside its transition catch.
			return ack, err
		}
		return ack, errors.Join(ErrRejected, err)
	}
	if c.kind == "alias_remove" {
		for _, v := range get(state, "aliases").([]any) {
			row := v.(*jsjson.Object)
			if get(row, "id") == c.targets.AliasIDs[0] && get(row, "scopeSceneId") != c.sceneID {
				return ack, ErrAliasUnavailable
			}
		}
	}
	if length, err := jsjson.UTF16Len(c.operationID); err != nil || length > 160 {
		return ack, ErrRejected
	}
	ack, err = writeTransition(session, state, exists, c.transition)
	if err != nil {
		return ack, errors.Join(ErrRejected, err)
	}
	return ack, nil
}
