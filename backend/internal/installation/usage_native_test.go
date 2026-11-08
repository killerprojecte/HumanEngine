//go:build humanengine_icu && cgo

package installation

import "testing"

func TestNativeSourceUsageBucketsRequired(t *testing.T) { testSourceUsageBuckets(t, true) }
