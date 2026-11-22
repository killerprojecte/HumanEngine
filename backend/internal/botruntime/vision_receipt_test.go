package botruntime

import (
	"context"
	"testing"
)

func TestNativeVisionReceiptRequiresExactLocalBody(t *testing.T) {
	const asset = "ob:media:receipt-fixture"
	body := fixtureMessage("scene", "image", "")
	_ = body.Set("parts", []any{obj("type", "media", "mediaType", "image/png", "assetId", asset)})
	normalized := body.Clone()
	_ = normalized.Set("origin", "history")
	remote := normalized.Clone()
	superseded := body.Clone()
	source := DisplayImageSource{record: &messageRecord{body: body}, assetID: asset}
	proofs := map[*Object]*Object{normalized: body, remote: superseded}
	ctx := context.WithValue(t.Context(), receiptImageSourceContextKey{}, &localImageReceipt{byRow: proofs})
	if !source.MatchesImageReceipt(ctx, normalized) || source.MatchesImageReceipt(ctx, remote) ||
		source.MatchesImageReceipt(ctx, normalized.Clone()) {
		t.Fatal("receipt confused local immutable body ownership with identical visible fields")
	}
	_ = normalized.Set("parts", []any{obj("type", "media", "mediaType", "image/png", "assetId", "ob:media:other")})
	if source.MatchesImageReceipt(ctx, normalized) {
		t.Fatal("receipt accepted a different asset for that retained body")
	}
}
