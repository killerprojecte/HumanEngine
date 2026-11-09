package snowluma

import "github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

// ConfigurationDecision preserves prepareConfiguration's ordered secret effects.
// The host executes one requested secret mutation then resumes; a failed first
// mutation must not delete fields or perform the second secret mutation.
func ConfigurationDecision(input *jsjson.Object) any {
	var state *jsjson.Object
	if textProperty(input, "operation") == "configuration_begin" {
		value, err := object(property(input, "config"))
		if err != nil {
			return httpJSON{"kind": "error", "code": "invalid_configuration"}
		}
		config := value.Clone()
		if !sameString(property(config, "transport"), "http") {
			previous, _ := object(property(input, "previous"))
			for _, key := range []string{"eventHost", "eventPort", "eventPath", "eventCredentialEnv"} {
				if previous != nil {
					if value, ok := previous.Get(key); ok {
						_ = config.Set(key, value)
						continue
					}
				}
				config.Delete(key)
			}
			config.Delete("eventToken")
			config.Delete("clearEventToken")
		}
		state = makeObject("config", config, "at", float64(0))
	} else {
		if truthy(property(input, "failed")) {
			return startupCredentialError(input)
		}
		var err error
		state, err = object(property(input, "state"))
		if err != nil {
			return httpJSON{"kind": "error", "code": "invalid_configuration_continuation"}
		}
		configurationFinishSecret(state)
	}
	config, _ := object(property(state, "config"))
	for at, _ := property(state, "at").(float64); at < 2; at, _ = property(state, "at").(float64) {
		key, clear, _ := configurationSecret(int(at))
		if property(config, clear) == true {
			return httpJSON{"kind": "secret_apply", "key": key, "action": "clear", "state": state}
		}
		if value, ok := property(config, key).(string); ok && value != "" {
			return httpJSON{"kind": "secret_apply", "key": key, "action": "set", "value": value, "state": state}
		}
		configurationFinishSecret(state)
	}
	return httpJSON{"kind": "configuration", "config": config}
}
func configurationSecret(at int) (string, string, string) {
	if at == 0 {
		return "token", "clearToken", "credentialEnv"
	}
	return "eventToken", "clearEventToken", "eventCredentialEnv"
}
func configurationFinishSecret(state *jsjson.Object) {
	at, _ := property(state, "at").(float64)
	config, _ := object(property(state, "config"))
	key, clear, legacy := configurationSecret(int(at))
	if property(config, clear) == true {
		config.Delete(legacy)
	}
	config.Delete(key)
	config.Delete(clear)
	_ = state.Set("at", at+1)
}
func startupCredentialError(input *jsjson.Object) any {
	code := "plugin_setup_failed"
	if property(input, "errorIsError") == true {
		switch textProperty(input, "errorMessage") {
		case "secret_key_unavailable", "secret_key_missing", "secret_key_permissions", "secret_key_invalid", "secret_decryption_failed":
			code = textProperty(input, "errorMessage")
		}
	}
	return httpJSON{"kind": "error", "code": code}
}

func ConfiguredSecretsDecision(input *jsjson.Object) any {
	if textProperty(input, "operation") == "configuration_secrets" {
		return httpJSON{"kind": "secret_has", "key": "token", "state": makeObject("at", float64(0), "secrets", new(jsjson.Object))}
	}
	state, err := object(property(input, "state"))
	if err != nil {
		return httpJSON{"kind": "error", "code": "invalid_secret_continuation"}
	}
	at, _ := property(state, "at").(float64)
	secrets, _ := object(property(state, "secrets"))
	key, _, _ := configurationSecret(int(at))
	_ = secrets.Set(key, property(input, "present") == true)
	if at == 0 {
		_ = state.Set("at", float64(1))
		return httpJSON{"kind": "secret_has", "key": "eventToken", "state": state}
	}
	return httpJSON{"kind": "configured_secrets", "secrets": secrets}
}
