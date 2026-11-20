//go:build web_read_candidate

package host

import (
	"testing"
	"time"
)

func requireDNSDrain(t *testing.T, job LookupJob) {
	t.Helper()
	select {
	case <-job.Drained:
	case <-time.After(3 * time.Second):
		t.Fatal("lookup did not drain")
	}
}
