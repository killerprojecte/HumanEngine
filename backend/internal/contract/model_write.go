package contract

import (
	"errors"
	"regexp"
	"strings"
	"unicode"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

var credentialID = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,160}$`)

// ParseModelWrite keeps credentials outside the public configuration schema.
// Every union branch is strict and the record retains JS property order.
func (s *Schemas) ParseModelWrite(raw []byte) (*jsjson.Object, *jsjson.Object, error) {
	value, e := jsjson.ParseBytes(raw)
	if e != nil {
		return nil, nil, e
	}
	o, ok := value.(*jsjson.Object)
	if !ok {
		return nil, nil, errors.New("invalid_model_write")
	}
	mutations := new(jsjson.Object)
	if v, has := o.Get("credentials"); has {
		mutations, ok = v.(*jsjson.Object)
		if !ok {
			return nil, nil, errors.New("invalid_credentials")
		}
		for _, m := range mutations.Members() {
			if !credentialID.MatchString(m.Key) {
				return nil, nil, errors.New("invalid_credential_id")
			}
			mutation, ok := m.Value.(*jsjson.Object)
			if !ok {
				return nil, nil, errors.New("invalid_credential_mutation")
			}
			a, _ := mutation.Get("action")
			switch a {
			case "preserve", "clear":
				if mutation.Len() != 1 {
					return nil, nil, errors.New("invalid_credential_mutation")
				}
			case "set":
				v, _ := mutation.Get("value")
				text, ok := v.(string)
				if !ok || mutation.Len() != 2 {
					return nil, nil, errors.New("invalid_credential_value")
				}
				n, e := jsjson.CodePointCount(text)
				if e != nil || n < 1 || n > 16384 || strings.TrimFunc(text, func(r rune) bool { return r == 0xfeff || r != '\u0085' && unicode.IsSpace(r) }) == "" {
					return nil, nil, errors.New("invalid_credential_value")
				}
			default:
				return nil, nil, errors.New("invalid_credential_action")
			}
		}
		o.Delete("credentials")
	}
	configRaw, e := jsjson.Marshal(o)
	if e != nil {
		return nil, nil, e
	}
	config, e := s.ParseOrdered("ModelConfig", configRaw)
	return config, mutations, e
}
