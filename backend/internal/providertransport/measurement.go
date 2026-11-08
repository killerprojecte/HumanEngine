package providertransport

import "math"

type CounterTarget struct{ ProviderID, Protocol, APIModelName, CodecID string }
type TextCounter interface {
	Identity() (id, revision string)
	Count([]byte) (float64, error)
}
type CounterRegistry interface {
	Lookup(CounterTarget) TextCounter
}
type Measurement struct {
	ProviderTokenCount string   `json:"providerTokenCount"`
	ProviderFraming    string   `json:"providerFraming"`
	HardTokenCap       bool     `json:"hardTokenCap"`
	BodyBytes          int      `json:"bodyBytes"`
	TextTokens         *float64 `json:"textTokens,omitempty"`
	TokenizerContract  string   `json:"tokenizerContract,omitempty"`
	Reason             string   `json:"reason"`
}

func Measure(body []byte, d Descriptor, codec string, registry CounterRegistry) (result Measurement) {
	result = Measurement{ProviderTokenCount: "unknown", ProviderFraming: "uncalibrated", BodyBytes: len(body), Reason: "unregistered_tokenizer"}
	if registry == nil {
		return
	}
	counter := registry.Lookup(CounterTarget{d.ProviderID, d.Protocol, d.APIModelName, codec})
	if counter == nil {
		return
	}
	defer func() {
		if recover() != nil {
			result.TextTokens = nil
			result.TokenizerContract = ""
			result.Reason = "counter_failed"
		}
	}()
	count, err := counter.Count(append([]byte(nil), body...))
	if err != nil {
		result.Reason = "counter_failed"
		return
	}
	if math.IsNaN(count) || math.IsInf(count, 0) || count < 0 || count > 9007199254740991 || math.Trunc(count) != count {
		result.Reason = "invalid_counter"
		return
	}
	id, revision := counter.Identity()
	result.TextTokens = &count
	result.TokenizerContract = id + "@" + revision
	result.Reason = "registered_text_counter"
	return
}
