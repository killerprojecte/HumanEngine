package observability

// Retention limits are source product policy, independent of asynchronous sink
// admission. Oversized records still go to sinks; only the retained view omits them.
const retainedRecordLimit = 500
const retainedByteLimit = 4 * 1024 * 1024

type retainedRecord struct {
	json  []byte
	level string
}
type retainedLogs struct {
	rows               []retainedRecord
	bytes              int
	evicted, oversized uint64
}

func (r *retainedLogs) add(data []byte, level string) {
	if len(data) > retainedByteLimit {
		r.oversized++
		return
	}
	r.rows = append(r.rows, retainedRecord{append([]byte(nil), data...), level})
	r.bytes += len(data)
	for len(r.rows) > retainedRecordLimit || r.bytes > retainedByteLimit {
		r.bytes -= len(r.rows[0].json)
		r.rows[0] = retainedRecord{}
		r.rows = r.rows[1:]
		r.evicted++
	}
}

type RetentionStatus struct {
	Records      int
	Bytes        int
	RecentErrors int
	Evicted      uint64
	Oversized    uint64
}

func (l *Logger) Retention() RetentionStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.retained.status()
}
func (r *retainedLogs) status() RetentionStatus {
	result := RetentionStatus{Records: len(r.rows), Bytes: r.bytes, Evicted: r.evicted, Oversized: r.oversized}
	for _, row := range r.rows {
		if row.level == "error" {
			result.RecentErrors++
		}
	}
	return result
}

// RetainedJSON returns an owned, newest-first snapshot of the retained Log
// projection. It does not read sinks or generate notices: Store.logs.toReversed()
// is a pure retained read, unlike Store.loggingStatus.
func (l *Logger) RetainedJSON() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.retained.snapshotJSON()
}
func (r *retainedLogs) snapshotJSON() []byte {
	result := make([]byte, 0, r.bytes+len(r.rows)+2)
	result = append(result, '[')
	for i := len(r.rows) - 1; i >= 0; i-- {
		if i != len(r.rows)-1 {
			result = append(result, ',')
		}
		data := r.rows[i].json
		// Ordinary retained records include the sink newline for byte accounting;
		// source-only degraded notices do not.
		if len(data) > 0 && data[len(data)-1] == '\n' {
			data = data[:len(data)-1]
		}
		result = append(result, data...)
	}
	return append(result, ']')
}
