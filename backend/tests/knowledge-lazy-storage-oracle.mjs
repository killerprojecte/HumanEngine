import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { KnowledgeStorage } from '../../src/memory/knowledge/storage.ts'
const db=new Database(':memory:')
let ready=0
const count=()=>db.prepare("SELECT count(*) AS n FROM sqlite_master WHERE name='memory_knowledge_edges' AND type='table'").get().n
const storage=new KnowledgeStorage(db,()=>{ready++})
assert.equal(count(),0)
assert.deepEqual(storage.rows('s'),[])
assert.equal(storage.get('missing'),undefined)
assert.equal(storage.byKey('missing'),undefined)
assert.equal(storage.entity('missing','s'),undefined)
assert.equal(count(),0)
assert.equal(ready,4)
console.log(JSON.stringify({source:'actual TS KnowledgeStorage with real in-memory SQLite',constructorTableCount:0,afterReadTableCount:count(),readyCalls:ready,reads:['rows','get','byKey','entity'],note:'Native Memory.Propose integration must prove the first real put; this oracle does not seed business rows.'},null,2))
db.close()
