//go:build !linux || !amd64 || !cgo

package pinnedhop

func RunResolverChild([]string) (bool, error) { return false, nil }
