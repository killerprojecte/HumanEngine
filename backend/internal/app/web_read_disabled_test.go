//go:build !web_read_candidate

package app

import (
	"context"
	"testing"
)

func TestWebReadDefaultApplicationAdmissionRemainsClosed(t *testing.T) {
	var a Application
	if err := a.bindWebRead(context.Background()); err == nil || err.Error() != "builtin_runtime_not_implemented" {
		t.Fatal(err)
	}
}
