package credentials

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"sync"
)

// Compiled schema metadata is immutable; configuration and credential presence
// are parsed/read anew on every call.
var modelReadSchemas = sync.OnceValues(contract.NewSchemas)

// ReadModelConfiguration is the source Store.models value projection. The
// presence callback must borrow its caller's configuration read owner; it must
// not fetch plaintext or open another root transaction. This helper has no IO.
func ReadModelConfiguration(raw []byte, has func(namespace, name string) (bool, error)) (*jsjson.Object, error) {
	schemas, e := modelReadSchemas()
	if e != nil {
		return nil, e
	}
	config, e := schemas.ParseOrdered("ModelConfig", raw)
	if e != nil {
		return nil, e
	}
	normalizeModelRoutes(config)
	for _, value := range modelField(config, "providers").([]any) {
		provider := value.(*jsjson.Object)
		namespace, name, e := keys("model", modelField(provider, "id").(string))
		if e != nil {
			return nil, e
		}
		exists, e := has(namespace, name)
		if e != nil {
			return nil, e
		}
		if e = provider.Set("credentialConfigured", exists); e != nil {
			return nil, e
		}
	}
	return config, nil
}
