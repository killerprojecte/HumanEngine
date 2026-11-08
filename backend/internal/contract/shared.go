package contract

import "sync"

var sharedSchemas = sync.OnceValues(NewSchemas)

// SharedSchemas returns immutable compiled contract metadata. It never caches
// configuration, credentials, parsed input, validation results or defaults.
// Parse and ParseOrdered return fresh values owned by each call. NewSchemas
// remains available when a caller deliberately needs a fresh compiler instance.
func SharedSchemas() (*Schemas, error) { return sharedSchemas() }
