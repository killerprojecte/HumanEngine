// Package logdelivery implements the native owner of the TypeScript log-writer
// contract. It does not format, retain, or expose Store logs over HTTP.
package logdelivery

import "time"

const DefaultTimeout = 5 * time.Second

// Status deliberately contains no operating-system error text or directory.
// LastFailure is nil, not an empty string, when the channel has no failure.
type Status struct {
	State              string  `json:"state"`
	Backpressured      bool    `json:"backpressured"`
	AcceptedRecords    int64   `json:"acceptedRecords"`
	WrittenRecords     int64   `json:"writtenRecords"`
	RejectedRecords    int64   `json:"rejectedRecords"`
	RejectedBytes      int64   `json:"rejectedBytes"`
	FailedRecords      int64   `json:"failedRecords"`
	UnconfirmedRecords int64   `json:"unconfirmedRecords"`
	PendingRecords     int64   `json:"pendingRecords"`
	PendingBytes       int64   `json:"pendingBytes"`
	MaxPendingBytes    int64   `json:"maxPendingBytes"`
	MaxPendingRecords  int64   `json:"maxPendingRecords"`
	LastFailure        *string `json:"lastFailure"`
}

type FlushResult struct {
	Complete               bool   `json:"complete"`
	Durable                bool   `json:"durable"`
	ThroughAcceptedRecords int64  `json:"throughAcceptedRecords"`
	FlushedAcceptedRecords int64  `json:"flushedAcceptedRecords"`
	Status                 Status `json:"status"`
}

func combined(current Status, previous *Status) Status {
	if previous == nil {
		return current
	}
	current.AcceptedRecords += previous.AcceptedRecords
	current.WrittenRecords += previous.WrittenRecords
	current.RejectedRecords += previous.RejectedRecords
	current.RejectedBytes += previous.RejectedBytes
	current.FailedRecords += previous.FailedRecords
	current.UnconfirmedRecords += previous.UnconfirmedRecords
	if current.LastFailure == nil {
		if previous.LastFailure != nil {
			reason := *previous.LastFailure
			current.LastFailure = &reason
		}
	}
	if current.LastFailure != nil {
		current.State = "degraded"
	}
	return current
}
