package observability

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"
)

// noticeLocked mirrors Store.loggingNotice: retain only, never recursively
// enqueue into a failed sink. Reading recent errors does not call this method.
func (l *Logger) noticeLocked(now time.Time) {
	if l.closedRejections == 0 && l.unserializable == 0 && l.retained.oversized == 0 && l.failedSinkRecords == 0 {
		return
	}
	if !l.noticeAt.IsZero() && now.Sub(l.noticeAt) < time.Minute {
		return
	}
	l.noticeAt = now
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	row := struct {
		ID     string `json:"id"`
		At     string `json:"at"`
		Level  string `json:"level"`
		Module string `json:"module"`
		Code   string `json:"code"`
		Counts struct {
			Rejected uint64 `json:"rejectedRecords"`
			Failed   uint64 `json:"failedRecords"`
		} `json:"counts"`
		Message string `json:"message"`
	}{
		ID: fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]),
		At: now.UTC().Format("2006-01-02T15:04:05.000Z"), Level: "warn", Module: "logging", Code: "log_delivery_degraded",
	}
	// Closed Emit rejections are Store rejections, not channel rejectedRecords.
	row.Counts.Failed = l.failedSinkRecords
	row.Message = fmt.Sprintf("logging · log_delivery_degraded；rejectedRecords=0；failedRecords=%d", row.Counts.Failed)
	data, err := json.Marshal(row)
	if err == nil {
		l.retained.add(data, "warn")
	} // source notice excludes trailing newline
}
func (l *Logger) encodingFailed() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.unserializable++
	l.noticeLocked(time.Now())
}
