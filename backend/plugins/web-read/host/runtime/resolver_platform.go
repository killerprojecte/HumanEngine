//go:build web_read_candidate && (!linux || !amd64 || !cgo)

package webreadruntime

import host "github.com/killerprojecte/HumanEngine/backend/plugins/web-read/host"

func (o *installation) openResolver() (host.Lookup, error) {
	resolver, err := host.NewOSResolver()
	if err != nil {
		return nil, err
	}
	o.resolver = resolver
	return resolver.Lookup, nil
}
