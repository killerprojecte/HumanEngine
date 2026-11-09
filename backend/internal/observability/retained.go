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
	result := RetentionStatus{Records: len(l.retained.rows), Bytes: l.retained.bytes, Evicted: l.retained.evicted, Oversized: l.retained.oversized}
	for _, row := range l.retained.rows {
		if row.level == "error" {
			result.RecentErrors++
		}
	}
	return result
}
