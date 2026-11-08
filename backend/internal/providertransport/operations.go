package providertransport

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
	"sort"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providerwire"
)

type EmbeddingModel struct {
	ID, ProviderID, APIModelName string
	Dimensions                   *int
	BatchSize                    *int
}
type EmbeddingResult struct {
	Vectors [][]float64
	Usage   map[string]any
	ModelID string
}

func (m *NativeModels) ListModels(ctx context.Context, providerID string) ([]string, error) {
	doneWork, ok := m.requests.Start()
	if !ok {
		return nil, safe("native_models_closed")
	}
	defer doneWork()
	if ctx == nil {
		ctx = context.Background()
	}
	if m.Policy == nil || m.Client == nil {
		return nil, safe("native_provider_composition_required")
	}
	provider, ok := m.Providers[providerID]
	if !ok {
		return nil, safe("provider_not_configured")
	}
	value, err := m.Policy.RunProviderOperation(ctx, providerID, "", "models", m.asyncInvocation(func(ctx context.Context, a modelservice.Attempt, done func(any, error)) {
		if !modelservice.IsBuiltin(provider.Protocol) {
			raw, e := m.customOperation(ctx, a, "listModels", nil)
			if e != nil {
				done(nil, e)
				return
			}
			values, ok := raw.([]any)
			if !ok || len(values) > 10000 {
				done(nil, safe("invalid_provider_response"))
				return
			}
			unique := map[string]bool{}
			for _, raw := range values {
				name, ok := raw.(string)
				length, err := jsjson.UTF16Len(name)
				if !ok || err != nil || name == "" || length > 200 {
					done(nil, safe("invalid_provider_response"))
					return
				}
				unique[name] = true
			}
			names := make([]string, 0, len(unique))
			for name := range unique {
				names = append(names, name)
			}
			sort.Slice(names, func(i, j int) bool { return utf16Less(names[i], names[j]) })
			done(names, nil)
			return
		}
		result, e := m.Client.Request(ctx, Request{Provider: provider, Path: "models"})
		if e != nil {
			done(nil, e)
			return
		}
		names, e := providerwire.ParseModels(result)
		done(names, e)
	}))
	if err != nil {
		return nil, err
	}
	return value.([]string), nil
}
func (m *NativeModels) Embeddings(ctx context.Context, model EmbeddingModel, inputs []string) (*EmbeddingResult, error) {
	doneWork, ok := m.requests.Start()
	if !ok {
		return nil, safe("native_models_closed")
	}
	defer doneWork()
	if ctx == nil {
		ctx = context.Background()
	}
	if m.Policy == nil || m.Client == nil {
		return nil, safe("native_provider_composition_required")
	}
	if model.Dimensions != nil && (*model.Dimensions < 1 || *model.Dimensions > 16384) {
		return nil, safe("invalid_embedding_dimensions_setting")
	}
	batchSize := 4
	if model.BatchSize != nil {
		batchSize = *model.BatchSize
	}
	if batchSize < 1 || batchSize > 64 {
		return nil, safe("invalid_embedding_batch_size")
	}
	provider, ok := m.Providers[model.ProviderID]
	if !ok {
		return nil, safe("provider_not_configured")
	}
	if active, ok := ctx.Value(laneKey{}).(laneContext); ok {
		ctx = modelservice.WithProviderOperationPriority(ctx, float64(laneRank(active.lane)))
	}
	pending := append([]string(nil), inputs...)
	var err error
	ctx, err = WithBudgetRequest(ctx)
	if err != nil {
		return nil, safe("provider_request_failed")
	}
	out := &EmbeddingResult{Vectors: [][]float64{}, Usage: map[string]any{"inputTokens": float64(0), "outputTokens": float64(0), "totalTokens": float64(0)}, ModelID: model.ID}
	for offset := 0; offset < len(pending); offset += batchSize {
		end := offset + batchSize
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[offset:end]
		value, e := m.Policy.RunProviderOperation(ctx, model.ProviderID, model.ID, "embedding", m.asyncInvocation(func(ctx context.Context, a modelservice.Attempt, done func(any, error)) {
			if !modelservice.IsBuiltin(provider.Protocol) {
				raw, e := m.customOperation(ctx, a, "embeddings", batch)
				if e != nil {
					done(nil, e)
					return
				}
				value, ok := raw.(map[string]any)
				if !ok {
					done(nil, safe("invalid_embedding_response"))
					return
				}
				rows, ok := value["vectors"].([]any)
				if !ok {
					done(nil, safe("invalid_provider_response"))
					return
				}
				data := make([]any, len(rows))
				for i, v := range rows {
					data[i] = map[string]any{"index": float64(i), "embedding": v}
				}
				vectors, _, e := providerwire.ParseEmbeddings(map[string]any{"data": data}, len(batch))
				if e != nil {
					done(nil, safe(e.Error()))
					return
				}
				usage, e := providerwire.NormalizedUsage(value["usage"])
				if e != nil {
					done(nil, safe(e.Error()))
					return
				}
				done(&EmbeddingResult{vectors, usage, model.ID}, nil)
				return
			}
			body := map[string]any{"model": model.APIModelName, "input": batch, "encoding_format": "float"}
			if model.Dimensions != nil {
				body["dimensions"] = *model.Dimensions
			}
			descriptor := Descriptor{ProviderID: provider.ID, ModelID: model.ID, Protocol: provider.Protocol, APIModelName: model.APIModelName, Operation: "embedding", Role: "embedding", Prices: m.Prices[model.ID]}
			result, e := m.Client.Request(ctx, Request{Provider: provider, Path: "embeddings", Body: body, Descriptor: &descriptor})
			if e != nil {
				done(nil, e)
				return
			}
			vectors, usage, e := providerwire.ParseEmbeddings(result, len(batch))
			if e != nil {
				done(nil, safe(e.Error()))
				return
			}
			done(&EmbeddingResult{vectors, usage, model.ID}, nil)
		}))
		if e != nil {
			return nil, e
		}
		result := value.(*EmbeddingResult)
		if ctx.Err() != nil {
			return nil, safe("request_cancelled")
		}
		if e = providerwire.ValidateEmbeddingVectors(result.Vectors, model.Dimensions); e != nil {
			return nil, safe(e.Error())
		}
		if len(out.Vectors) > 0 {
			for _, v := range result.Vectors {
				if len(v) != len(out.Vectors[0]) {
					return nil, safe("invalid_embedding_dimensions")
				}
			}
		}
		out.Vectors = append(out.Vectors, result.Vectors...)
		mergeUsage(out.Usage, result.Usage, offset == 0)
	}
	return out, nil
}
func validSum(a, b float64) (float64, bool) {
	sum := a + b
	return sum, !math.IsNaN(sum) && !math.IsInf(sum, 0) && sum >= 0 && sum <= 9007199254740991 && math.Trunc(sum) == sum
}
func mergeUsage(dst, src map[string]any, first bool) {
	for _, k := range []string{"inputTokens", "outputTokens", "totalTokens"} {
		a, _ := dst[k].(float64)
		b, _ := src[k].(float64)
		sum, ok := validSum(a, b)
		if !ok {
			dst[k] = float64(0)
			dst["known"] = false
		} else {
			dst[k] = sum
		}
	}
	for _, k := range []string{"ordinaryInputTokens", "cacheReadInputTokens", "cacheWriteInputTokens", "cacheWrite5mInputTokens", "cacheWrite1hInputTokens", "cacheWrite30mInputTokens", "cacheWrite12hInputTokens"} {
		b, bok := src[k].(float64)
		a, aok := dst[k].(float64)
		if first && bok {
			dst[k] = b
		} else if aok && bok {
			sum, ok := validSum(a, b)
			if ok {
				dst[k] = sum
			} else {
				delete(dst, k)
				dst["known"] = false
			}
		} else {
			delete(dst, k)
		}
	}
	raw, ok := src["rawTokenCounters"].(map[string]any)
	if first && ok {
		copy := map[string]any{}
		for k, v := range raw {
			copy[k] = v
		}
		dst["rawTokenCounters"] = copy
	} else if prior, pok := dst["rawTokenCounters"].(map[string]any); ok && pok {
		for k, v := range prior {
			a, _ := v.(float64)
			b, bok := raw[k].(float64)
			sum, valid := validSum(a, b)
			if !bok || !valid {
				delete(prior, k)
			} else {
				prior[k] = sum
			}
		}
	} else {
		delete(dst, "rawTokenCounters")
	}
	cache, hasCache := src["cacheKnown"]
	if first && hasCache {
		dst["cacheKnown"] = cache
	} else if _, prior := dst["cacheKnown"]; prior || hasCache {
		dst["cacheKnown"] = dst["cacheKnown"] == true && cache == true
	}
	if src["known"] == false {
		dst["known"] = false
	}
	if _, exists := dst["cacheKnown"]; exists && dst["known"] == false {
		dst["cacheKnown"] = false
	}
}

func utf16Less(a, b string) bool {
	x, _ := jsjson.UTF16(a)
	y, _ := jsjson.UTF16(b)
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return len(x) < len(y)
}
