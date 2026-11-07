package scheduler

import (
	"context"
	"errors"
	"math"
)

var (
	ErrInvalidModelPriority    = errors.New("invalid_model_priority")
	ErrInvalidLearningDeadline = errors.New("invalid_learning_deadline")
)

type priorityKey struct{}
type learningKey struct{}

// WithModelPriority can lower urgency but cannot elevate an inherited background
// scope. The original context remains unchanged, so scopes cannot leak.
func WithModelPriority(ctx context.Context, priority float64) (context.Context, error) {
	if priority != 1 && priority != 2 {
		return nil, ErrInvalidModelPriority
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if inherited, ok := ctx.Value(priorityKey{}).(float64); ok {
		priority = math.Max(priority, inherited)
	}
	return context.WithValue(ctx, priorityKey{}, priority), nil
}
func EffectiveModelPriority(ctx context.Context, basePriority float64) float64 {
	if ctx != nil {
		if priority, ok := ctx.Value(priorityKey{}).(float64); ok {
			return math.Max(basePriority, priority)
		}
	}
	return basePriority
}

// WithOverdueLearning preserves the later nested deadline. A finite fractional
// timestamp is valid, exactly as in the source; it is not rounded or clamped.
func WithOverdueLearning(ctx context.Context, notBefore float64) (context.Context, error) {
	if math.IsNaN(notBefore) || math.IsInf(notBefore, 0) || notBefore < 0 {
		return nil, ErrInvalidLearningDeadline
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if inherited, ok := GetOverdueLearningAt(ctx); ok {
		notBefore = math.Max(notBefore, inherited)
	}
	return context.WithValue(ctx, learningKey{}, notBefore), nil
}
func GetOverdueLearningAt(ctx context.Context) (float64, bool) {
	if ctx == nil {
		return 0, false
	}
	v, ok := ctx.Value(learningKey{}).(float64)
	return v, ok
}
