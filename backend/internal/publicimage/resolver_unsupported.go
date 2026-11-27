//go:build !linux || !amd64 || !cgo

package publicimage

func NewReader(func() (func(), bool)) *Reader { return nil }
