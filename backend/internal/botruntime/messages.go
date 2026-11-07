package botruntime

import "math"

// Block width controls structural sharing only, never message admission. Each
// immutable record retains its once-parsed source timestamp beside the body.
const messageBlockWidth = 32

type messageRecord struct {
	body       *Object
	receivedAt float64
}
type messageBlock struct {
	previous *messageBlock
	rows     []*Object
	records  []*messageRecord
	count    int
}

func appendRecord(root *messageBlock, record *messageRecord) *messageBlock {
	if root == nil {
		return &messageBlock{rows: []*Object{record.body}, records: []*messageRecord{record}, count: 1}
	}
	if len(root.rows) < messageBlockWidth {
		return &messageBlock{root.previous, append(append([]*Object{}, root.rows...), record.body), append(append([]*messageRecord{}, root.records...), record), root.count + 1}
	}
	return &messageBlock{root, []*Object{record.body}, []*messageRecord{record}, root.count + 1}
}
func bodyRows(root *messageBlock) []*Object {
	if root == nil {
		return []*Object{}
	}
	r := make([]*Object, root.count)
	at := len(r)
	for b := root; b != nil; b = b.previous {
		at -= len(b.rows)
		copy(r[at:], b.rows)
	}
	return r
}
func bodyRecords(root *messageBlock) []*messageRecord {
	if root == nil {
		return []*messageRecord{}
	}
	r := make([]*messageRecord, root.count)
	at := len(r)
	for b := root; b != nil; b = b.previous {
		at -= len(b.records)
		copy(r[at:], b.records)
	}
	return r
}
func (r *Runtime) appendMessageLocked(s *sceneState, m *Object) {
	record := &messageRecord{m, r.date(str(get(m, "receivedAt")))}
	s.parsedBodies++
	s.byID[idOf(m)] = record
	s.byBody[m] = record
	s.available[idOf(m)] = get(m, "status") == "available"
	s.messages = append(s.messages, m)
	s.history = appendRecord(s.history, record)
	s.earliest = math.Min(s.earliest, record.receivedAt)
	if !math.IsNaN(record.receivedAt) {
		s.historyIDs.Set(idOf(m), record.receivedAt)
		s.historyEarliest = math.Min(s.historyEarliest, record.receivedAt)
	}
}
func (r *Runtime) replaceRecordLocked(s *sceneState, old, next *Object) {
	previous := s.byBody[old]
	at := previous.receivedAt
	if get(old, "receivedAt") != get(next, "receivedAt") {
		at = r.date(str(get(next, "receivedAt")))
		s.parsedBodies++
	}
	record := &messageRecord{next, at}
	delete(s.byBody, old)
	s.byBody[next] = record
	s.byID[idOf(next)] = record
	s.available[idOf(next)] = get(next, "status") == "available"
	s.historyDirty = true
}
func (r *Runtime) rebuildMessagesLocked(s *sceneState) {
	byID := map[string]*messageRecord{}
	byBody := map[*Object]*messageRecord{}
	available := map[string]bool{}
	s.history = nil
	s.earliest = math.Inf(1)
	for _, m := range s.messages {
		record := s.byBody[m]
		if record == nil {
			panic("message_record_missing")
		}
		byID[idOf(m)] = record
		byBody[m] = record
		available[idOf(m)] = get(m, "status") == "available"
		s.history = appendRecord(s.history, record)
		s.earliest = math.Min(s.earliest, record.receivedAt)
	}
	s.byID, s.byBody, s.available = byID, byBody, available
}
