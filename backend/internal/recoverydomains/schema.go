package recoverydomains

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"sort"
	"strings"
)

const habitDDL = `CREATE TABLE IF NOT EXISTS habits (id TEXT PRIMARY KEY, fingerprint TEXT UNIQUE NOT NULL, value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS habit_deleted (fingerprint TEXT PRIMARY KEY);
      `
const memeDDL = `CREATE TABLE IF NOT EXISTS meme_assets (id TEXT PRIMARY KEY, value TEXT NOT NULL);
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

      `
const schemaDDL = habitDDL + memeDDL + experienceDDL

var schemas = map[string][]string{"habits": {"id", "fingerprint", "value"}, "habit_deleted": {"fingerprint"}, "meme_assets": {"id", "value"}, "meme_reference_denials": {"key"}, "meme_sources": {"asset_id", "scene_id", "message_id", "sender_id"}, "meme_media_references": {"scene_id", "reference_hash", "asset_id", "message_id", "sender_id", "content_hash", "updated_at"}, "meme_feedback": {"asset_id", "scene_id", "message_id", "positive"}, "meme_search_concepts": {"asset_id", "concept"}, "memory_experience_outcomes": {"id", "root", "scene", "source", "value"}, "memory_experience_policies": {"id", "scene", "version", "value"}, "memory_experience_validations": {"id", "policy", "value"}, "memory_experience_uses": {"root", "scene", "policy", "target", "value"}, "memory_experience_tombstones": {"key"}, "memory_experience_trials": {"policy", "scene", "version", "value"}}

func assertSchema(ctx context.Context, tx *recovery.MaintenanceTx) error {
	rows, e := tx.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table'")
	if e != nil {
		return e
	}
	names := []string{}
	for rows.Next() {
		var n string
		if e = rows.Scan(&n); e != nil {
			rows.Close()
			return e
		}
		names = append(names, n)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, n := range names {
		if n != "habits" && !strings.HasPrefix(n, "habit_") && !strings.HasPrefix(n, "meme_") && !strings.HasPrefix(n, "experience_") && !strings.HasPrefix(n, "memory_experience_") {
			continue
		}
		expected, ok := schemas[n]
		if !ok {
			return errors.New("recovery_unknown_domain_schema")
		}
		r, e := tx.QueryContext(ctx, "PRAGMA table_info(\""+n+"\")")
		if e != nil {
			return e
		}
		cols := []string{}
		for r.Next() {
			var cid, notnull, pk int
			var name, kind string
			var d any
			if e = r.Scan(&cid, &name, &kind, &notnull, &d, &pk); e != nil {
				r.Close()
				return e
			}
			cols = append(cols, name)
		}
		e = r.Err()
		r.Close()
		if e != nil {
			return e
		}
		sort.Strings(cols)
		want := append([]string{}, expected...)
		sort.Strings(want)
		if strings.Join(cols, "\x00") != strings.Join(want, "\x00") {
			return errors.New("recovery_unknown_domain_schema")
		}
	}
	return nil
}

const experienceDDL = `
      CREATE TABLE IF NOT EXISTS memory_experience_outcomes(id TEXT PRIMARY KEY,root TEXT UNIQUE NOT NULL,scene TEXT NOT NULL,source TEXT NOT NULL,value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS memory_experience_policies(id TEXT PRIMARY KEY,scene TEXT NOT NULL,version INTEGER NOT NULL,value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS memory_experience_validations(id TEXT PRIMARY KEY,policy TEXT NOT NULL,value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS memory_experience_uses(root TEXT PRIMARY KEY,scene TEXT NOT NULL,policy TEXT NOT NULL,target TEXT,value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS memory_experience_tombstones(key TEXT PRIMARY KEY);
      CREATE INDEX IF NOT EXISTS memory_experience_outcome_source ON memory_experience_outcomes(scene,source);
      CREATE INDEX IF NOT EXISTS memory_experience_policy_scene ON memory_experience_policies(scene);
    `
