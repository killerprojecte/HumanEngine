//go:build humanengine_icu && cgo

package nodeicu

func InitializationRunsForTest() uint32 { return initialization.runs.Load() }
