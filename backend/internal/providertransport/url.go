package providertransport

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	whatwg "github.com/nlnwa/whatwg-url/url"
)

// Match new URL(provider.baseUrl), including USVString conversion, path
// normalization and empty query/fragment markers. Search()/Hash() intentionally
// distinguish nonempty values from an empty terminal ? or #, as JavaScript does.
func providerBaseURL(value string) (string, error) {
	text, e := jsjson.UTF8Text(value)
	if e != nil {
		return "", safe("invalid_provider_url")
	}
	base, e := whatwg.Parse(text)
	if e != nil || (base.Protocol() != "http:" && base.Protocol() != "https:") || base.Username() != "" || base.Password() != "" || base.Search() != "" || base.Hash() != "" {
		return "", safe("invalid_provider_url")
	}
	return base.Href(false), nil
}
