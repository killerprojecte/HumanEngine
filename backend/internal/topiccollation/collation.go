// Package topiccollation supplies the pinned native implementation of the
// source Node deployment's String.localeCompare, without starting Node.
package topiccollation

import "errors"

var (
	ErrUnavailable = errors.New("topiccollation: binary was built without pinned native ICU")
	ErrClosed      = errors.New("topiccollation: collator is closed")
	ErrLocale      = errors.New("topiccollation: explicit source-resolved BCP47 locale required")
)

const (
	SourceNode          = "v24.22.1"
	SourceArchiveSHA256 = "4f5379796280cdaeb0c2f358fd04b6a16cc4fb7dfa83b5a8c91a9d963b435231"
	DataSHA256          = "f294f56707f4bbbc669d291ff537f632eb8d363a8ea60b70b091cdf273ea0499"
)

// Info records the data and effective ICU attributes checked at initialization.
// RequestedLocale must come from the source deployment's resolved default
// Intl.Collator locale, not this process's LANG or a hard-coded universal locale.
type Info struct {
	RequestedLocale   string
	ValidLocale       string
	ActualLocale      string
	ICU               string
	Unicode           string
	CLDR              string
	Data              string
	Collator          string
	UCA               string
	Normalization     int
	Strength          int
	Numeric           int
	CaseFirst         int
	AlternateHandling int
	CaseLevel         int
}
