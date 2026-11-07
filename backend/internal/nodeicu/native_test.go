//go:build humanengine_icu && cgo

package nodeicu_test

import (
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"os"
	"os/exec"
	"sync"
	"testing"
)

func TestConcurrentConsumersShareOneInitializer(t *testing.T) {
	if os.Getenv("HE_NODEICU_FIRST_USE") != "1" {
		command := exec.Command(os.Args[0], "-test.run=^TestConcurrentConsumersShareOneInitializer$")
		command.Env = append(os.Environ(), "HE_NODEICU_FIRST_USE=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fresh-process first-use regression: %v\n%s", err, output)
		}
		return
	}
	if n := nodeicu.InitializationRunsForTest(); n != 0 {
		t.Fatalf("unexpected eager initialization: %d", n)
	}
	start := make(chan struct{})
	errors := make(chan error, 128)
	var done sync.WaitGroup
	for i := 0; i < 128; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			<-start
			if i%2 == 0 {
				c, err := topiccollation.New("en-US")
				if err == nil {
					defer c.Close()
					var result int
					result, err = c.Compare("a", "b")
					if err == nil && result >= 0 {
						err = fmt.Errorf("wrong collation")
					}
				}
				errors <- err
			} else {
				p, err := jsdate.New("America/New_York")
				if err == nil {
					var finite bool
					finite, err = p.Finite("2026-11-07T12:00:00")
					if err == nil && !finite {
						err = fmt.Errorf("wrong date result")
					}
				}
				errors <- err
			}
		}(i)
	}
	close(start)
	done.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Error(err)
		}
	}
	if n := nodeicu.InitializationRunsForTest(); n != 1 {
		t.Fatalf("initialization ran %d times", n)
	}
}
