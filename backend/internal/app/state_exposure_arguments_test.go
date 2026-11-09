package app

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"testing"
)

func TestStateExposureSourceArgumentBoundaries(t *testing.T) {
	cases := []struct {
		name, body string
		valid      bool
	}{
		{"memory.history", `{"memoryId":"a","asOfRecorded":"not-a-date"}`, true},
		{"memory.history", `{"memoryId":"a","anchor":{}}`, false},
		{"memory.history", `{"memoryId":""}`, false},
		{"memory.history", `{"memoryId":"a","limit":0}`, false},
		{"memory.history", `{"memoryId":"a","limit":20}`, true},
		{"memory.history", `{"memoryId":"a","limit":21}`, false},
		{"memory.commitments", `{"personId":17,"unknown":true}`, true},
		{"memory.commitments", `{"lifecycle":null}`, false},
		{"memory.commitments", `{"lifecycle":"terminal"}`, true},
		{"memory.commitmentHistory", `{"id":"p","version":1,"deletionEpoch":0}`, true},
		{"memory.commitmentHistory", `{"id":"p","version":1.5,"deletionEpoch":0}`, false},
		{"memory.commitmentHistory", `{"id":"p","version":9007199254740992,"deletionEpoch":0}`, false},
		{"memory.commitmentTimeline", `{"id":"p","version":1,"deletionEpoch":0,"asOfRecorded":"2024-02-29T12:00:00.123456789012Z"}`, true},
		{"memory.commitmentTimeline", `{"id":"p","version":1,"deletionEpoch":0,"asOfRecorded":"0000-02-29T12:00:00Z"}`, true},
		{"memory.commitmentTimeline", `{"id":"p","version":1,"deletionEpoch":0,"asOfRecorded":"2023-02-29T12:00:00Z"}`, false},
		{"memory.commitmentTimeline", `{"id":"p","version":1,"deletionEpoch":0,"asOfRecorded":"2024-02-29T12:00Z"}`, false},
		{"memory.commitmentTimeline", `{"id":"p","version":1,"deletionEpoch":0,"asOfRecorded":"2024-02-29T12:00:00+00:00"}`, false},
	}
	for _, test := range cases {
		t.Run(test.name+test.body, func(t *testing.T) {
			input, e := jsjson.Parse(test.body)
			if e != nil {
				t.Fatal(e)
			}
			_, e = stateExposureArguments(test.name, input)
			if (e == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, e)
			}
		})
	}
}
