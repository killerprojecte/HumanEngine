//go:build !humanengine_icu || !cgo

package topiccollation

// Collator is unavailable unless built with the pinned native dependency.
type Collator struct{}

func New(sourceResolvedLocale string) (*Collator, error) { return nil, ErrUnavailable }
func (*Collator) Compare(a, b string) (int, error)       { return 0, ErrUnavailable }
func (*Collator) Close() error                           { return nil }
func (*Collator) Info() Info                             { return Info{} }
