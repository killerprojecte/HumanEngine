package assertions

// ManualCommandReceipt deliberately contains only retry metadata, never bodies.
type ManualCommandReceipt struct {
	AssertionID, PayloadHash     string
	DeletionEpoch, ResultVersion int64
}

func (s *Store) EnsureManualCommands() error {
	if e := s.requireTransaction(); e != nil {
		return e
	}
	return s.session.EnsureManualCommandSchema()
}
func (r *Reader) ManualReceipt(key string) (*ManualCommandReceipt, error) {
	if e := r.check(); e != nil {
		return nil, e
	}
	v := new(ManualCommandReceipt)
	found, e := r.one("SELECT assertion_id,payload_hash,deletion_epoch,result_version FROM memory_command_receipts WHERE command_key=?", []any{key}, &v.AssertionID, &v.PayloadHash, &v.DeletionEpoch, &v.ResultVersion)
	if e != nil || !found {
		return nil, e
	}
	return v, nil
}
func (s *Store) SaveManualReceipt(key string, v ManualCommandReceipt, at string) error {
	if e := s.exec("INSERT INTO memory_command_receipts VALUES(?,?,?,?,?,?)", key, v.AssertionID, v.PayloadHash, v.DeletionEpoch, v.ResultVersion, at); e != nil {
		return e
	}
	// Existing source policy, not a new admission/resource cap. Qualify both
	// tables: SQLite otherwise emits an ambiguous blank-database READ for
	// this rowid-only subquery. Do not weaken the owner authorizer.
	return s.exec("DELETE FROM main.memory_command_receipts WHERE rowid NOT IN (SELECT rowid FROM main.memory_command_receipts ORDER BY rowid DESC LIMIT 4096)")
}
func (s *Store) DeleteManualSupports(id string) error {
	return s.exec("DELETE FROM memory_supports WHERE memory_id=?", id)
}
func (s *Store) RememberManualFingerprint(record *Object, at string) error {
	fp, e := Fingerprint(record)
	if e != nil {
		return e
	}
	return s.exec("INSERT OR REPLACE INTO memory_deleted VALUES(?,?)", fp, at)
}
func (r *Reader) HasManualFingerprint(fp string) (bool, error) {
	if e := r.check(); e != nil {
		return false, e
	}
	var one int
	return r.one("SELECT 1 FROM memories WHERE fingerprint=?", []any{fp}, &one)
}
