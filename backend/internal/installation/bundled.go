package installation

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/vectorasset"
)

// OpenBundled obtains a genuine pinned native extension from this executable.
// No source/dependency directory or runtime download is required. The current
// verified target is linux/amd64 with bundled_sqlite_vec. Unbundled/unknown
// targets fail before opening main or reserving an authority.
func OpenBundled(ctx context.Context, o Options) (*Store, error) {
	if o.VectorExtension != "" {
		return nil, errors.New("sqlite_vec_bundle_explicit_extension_conflict")
	}
	asset, err := vectorasset.Extract()
	if err != nil {
		return nil, err
	}
	s, err := open(ctx, o, asset)
	if err != nil {
		return nil, errors.Join(err, asset.Close())
	}
	return s, nil
}
