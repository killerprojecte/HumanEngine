//go:build !humanengine_icu || !cgo

package topiccollation

type RequestedLocale struct{}

func NewRequested(string) (*RequestedLocale, error)     { return nil, ErrUnavailable }
func (*RequestedLocale) Close()                         {}
func (*RequestedLocale) Lower(string) (string, error)   { return "", ErrUnavailable }
func (*RequestedLocale) SortKey(string) ([]byte, error) { return nil, ErrUnavailable }
