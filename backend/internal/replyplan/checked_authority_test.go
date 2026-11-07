package replyplan

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestCheckedEligibilityRetainsOriginalError(t *testing.T) {
	for _, at := range []int{1, 2} {
		t.Run(fmt.Sprint(at), func(t *testing.T) {
			cause := errors.New("synthetic_source_sql_failed")
			checks, models := 0, 0
			input := BuildInput{
				Eligible: func() bool { t.Fatal("legacy eligible called"); return false },
				EligibleChecked: func() (bool, error) {
					checks++
					if checks == at {
						return false, cause
					}
					return true, nil
				},
				Complete: func(_ context.Context, _ []*Object, _ []*Object, settle func(Completion, error)) {
					models++
					settle(Completion{}, nil)
				},
			}
			_, err := (&Policy{}).BuildReplyPlan(context.Background(), input)
			if !errors.Is(err, cause) || checks != at || models != at-1 {
				t.Fatal(err, checks, models)
			}
		})
	}
	_, err := (&Policy{}).BuildReplyPlan(context.Background(), BuildInput{EligibleChecked: func() (bool, error) { return false, nil }})
	if err == nil || err.Error() != "planner_context_ineligible" {
		t.Fatal(err)
	}
}
