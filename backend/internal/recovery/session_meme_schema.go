package recovery

func (s *WriteSession) EnsureMemeStorageSchema() error {
	if e := s.AssertReady(); e != nil {
		return e
	}
	return s.schema(`CREATE TABLE IF NOT EXISTS meme_assets (id TEXT PRIMARY KEY, value TEXT NOT NULL);
      CREATE INDEX IF NOT EXISTS meme_content_hash_lookup
        ON meme_assets(COALESCE(json_extract(value,'$.contentHash'),id));
      CREATE TABLE IF NOT EXISTS meme_sources (
        asset_id TEXT NOT NULL REFERENCES meme_assets(id) ON DELETE CASCADE,
        scene_id TEXT NOT NULL, message_id TEXT NOT NULL, sender_id TEXT NOT NULL,
        PRIMARY KEY(asset_id,scene_id,message_id)
      );
      CREATE INDEX IF NOT EXISTS meme_source_lookup ON meme_sources(scene_id,message_id);
      CREATE TABLE IF NOT EXISTS meme_reference_denials (key TEXT PRIMARY KEY);
      CREATE TABLE IF NOT EXISTS meme_media_references (
        scene_id TEXT NOT NULL, reference_hash TEXT NOT NULL,
        asset_id TEXT NOT NULL, message_id TEXT NOT NULL, sender_id TEXT NOT NULL,
        content_hash TEXT NOT NULL, updated_at INTEGER NOT NULL,
        PRIMARY KEY(scene_id,reference_hash,message_id)
      );
      CREATE TABLE IF NOT EXISTS meme_feedback (
        asset_id TEXT NOT NULL REFERENCES meme_assets(id) ON DELETE CASCADE,
        scene_id TEXT NOT NULL, message_id TEXT NOT NULL, positive INTEGER NOT NULL,
        PRIMARY KEY(asset_id,scene_id,message_id)
      );
      CREATE TABLE IF NOT EXISTS meme_search_concepts (
        asset_id TEXT NOT NULL REFERENCES meme_assets(id) ON DELETE CASCADE,
        concept TEXT NOT NULL, PRIMARY KEY(asset_id,concept)
      );
      CREATE INDEX IF NOT EXISTS meme_concept_lookup ON meme_search_concepts(concept,asset_id);

      `)
}
