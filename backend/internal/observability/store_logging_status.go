package observability

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability/logdelivery"
)

type StoreMemoryStatus struct {
	RetainedRecords  int    `json:"retainedRecords"`
	RetainedBytes    int    `json:"retainedBytes"`
	EvictedRecords   uint64 `json:"evictedRecords"`
	OversizedRecords uint64 `json:"oversizedRecords"`
	MaxBytes         int    `json:"maxBytes"`
	MaxRecords       int    `json:"maxRecords"`
}
type StoreLoggingStatus struct {
	File                  *logdelivery.Status `json:"file"`
	Stdout                *logdelivery.Status `json:"stdout"`
	InitializationFailed  bool                `json:"initializationFailed"`
	UnserializableRecords uint64              `json:"unserializableRecords"`
	Closed                bool                `json:"closed"`
	ShutdownComplete      *bool               `json:"shutdownComplete"`
	ClosedRejectedRecords uint64              `json:"closedRejectedRecords"`
	Memory                StoreMemoryStatus   `json:"memory"`
}

func snapshotLease(lease logLease) *logdelivery.Status {
	if lease == nil {
		return nil
	}
	status := lease.Status()
	return &status
}
func (l *StoreLogger) LoggingStatus() StoreLoggingStatus {
	l.loggingNotice()
	l.mu.Lock()
	file, out := l.file, l.stdout
	result := StoreLoggingStatus{InitializationFailed: l.initializationFailed, UnserializableRecords: l.unserializable, Closed: l.closed, ClosedRejectedRecords: l.closedRejected, Memory: StoreMemoryStatus{len(l.retained.rows), l.retained.bytes, l.retained.evicted, l.retained.oversized, retainedByteLimit, retainedRecordLimit}}
	if l.shutdownComplete != nil {
		value := *l.shutdownComplete
		result.ShutdownComplete = &value
	}
	l.mu.Unlock()
	result.File = snapshotLease(file)
	result.Stdout = snapshotLease(out)
	return result
}
func (l *StoreLogger) loggingNotice() {
	l.mu.Lock()
	out, file := l.stdout, l.file
	l.mu.Unlock()
	stdoutStatus, fileStatus := snapshotLease(out), snapshotLease(file)
	degraded := false
	var rejected, failed int64
	for _, status := range []*logdelivery.Status{stdoutStatus, fileStatus} {
		if status != nil {
			degraded = degraded || status.State == "degraded"
			rejected += status.RejectedRecords
			failed += status.FailedRecords
		}
	}
	now := l.now()
	l.mu.Lock()
	degraded = degraded || l.initializationFailed || l.closedRejected > 0 || l.unserializable > 0 || l.retained.oversized > 0
	if !degraded || (l.noticeSet && now.UnixMilli()-l.noticeAt < 60000) {
		l.mu.Unlock()
		return
	}
	l.noticeAt = now.UnixMilli()
	l.noticeSet = true
	l.mu.Unlock()
	id, err := l.newID()
	if err != nil {
		return
	}
	counts, _ := jsjson.NewObject(jsjson.Member{Key: "rejectedRecords", Value: float64(rejected)}, jsjson.Member{Key: "failedRecords", Value: float64(failed)})
	context, _ := jsjson.NewObject(jsjson.Member{Key: "counts", Value: counts})
	row := sourceLogRow(Event{ID: id, At: now, Level: "warn", Module: "logging", Code: "log_delivery_degraded"}, context)
	bytes, err := jsjson.Marshal(row)
	if err != nil {
		return
	}
	l.mu.Lock()
	l.retained.add(bytes, "warn")
	l.mu.Unlock() // no notice newline and no recursive sink admission
}
