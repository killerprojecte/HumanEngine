package snowstop

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

func TestComposeSnowLumaPartialACK(t *testing.T) {
	bind := os.Getenv("HE_COMPOSE_FIXTURE_BIND")
	if bind == "" {
		t.Skip("requires disposable Compose fixture runner")
	}
	decoder := json.NewDecoder(bufio.NewReader(os.Stdin))
	encoder := json.NewEncoder(os.Stdout)
	exchange := func(phase string) string {
		t.Helper()
		if err := encoder.Encode(phase); err != nil {
			t.Fatal(err)
		}
		var reply string
		if err := decoder.Decode(&reply); err != nil {
			t.Fatal(err)
		}
		return reply
	}
	Run(t, bind, os.Getenv("HUMANENGINE_ADMIN_TOKEN"), false, func() Process {
		address := exchange("start")
		return Process{Address: func() string { return address }, Stop: func() string { return exchange("stop") }}
	})
}
