package snowluma

import "github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

// StartupDecision starts only after source-compatible schema parsing. Credential
// lookup is ordered token then eventToken (including WebSocket); nullish fallback
// differs from an empty stored credential. Environment access is requested IO.
func StartupDecision(input *jsjson.Object) any {
	operation := textProperty(input, "operation")
	if operation == "startup_begin" {
		config, err := object(property(input, "config"))
		if err != nil {
			return httpJSON{"kind": "error", "code": "invalid_startup_config"}
		}
		return httpJSON{"kind": "secret_get", "key": "token", "state": makeObject("config", config, "at", float64(0))}
	}
	state, err := object(property(input, "state"))
	if err != nil {
		return httpJSON{"kind": "error", "code": "invalid_startup_continuation"}
	}
	config, _ := object(property(state, "config"))
	at, _ := property(state, "at").(float64)
	key, _, legacy := configurationSecret(int(at))
	if operation == "startup_secret" {
		if truthy(property(input, "failed")) {
			return startupCredentialError(input)
		}
		if property(input, "present") != true {
			if name := property(config, legacy); truthy(name) {
				return httpJSON{"kind": "environment_get", "name": name, "state": state}
			}
		}
	}
	value := property(input, "value")
	if operation == "startup_secret" && property(input, "present") != true {
		value = missing{}
	}
	if at == 0 && !truthy(value) {
		return httpJSON{"kind": "error", "code": "credential_not_configured"}
	}
	if at == 1 && sameString(property(config, "transport"), "http") && !truthy(value) {
		return httpJSON{"kind": "error", "code": "event_credential_not_configured"}
	}
	if _, absent := value.(missing); !absent {
		_ = state.Set(key, value)
	}
	if at == 0 {
		_ = state.Set("at", float64(1))
		return httpJSON{"kind": "secret_get", "key": "eventToken", "state": state}
	}
	plan := httpJSON{"kind": "install", "config": config}
	for _, name := range []string{"token", "eventToken"} {
		if v, exists := state.Get(name); exists {
			plan[name] = v
		}
	}
	return plan
}
