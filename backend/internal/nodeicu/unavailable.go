//go:build !humanengine_icu || !cgo

package nodeicu

func Ensure() error { return ErrUnavailable }
