//go:build !bundled_sqlite_vec || !linux || !amd64

package vectorasset

import (
	"fmt"
	"runtime"
)

func Extract() (*Asset, error) {
	return nil, fmt.Errorf("sqlite_vec_bundle_unavailable: target %s/%s requires a verified bundled_sqlite_vec asset", runtime.GOOS, runtime.GOARCH)
}
