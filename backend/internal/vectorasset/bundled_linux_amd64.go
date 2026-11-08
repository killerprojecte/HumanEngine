//go:build bundled_sqlite_vec && linux && amd64

package vectorasset

import _ "embed"

//go:embed data/linux-amd64/vec0.so
var library []byte

func bundledBytes() ([]byte, error) { return library, nil }
