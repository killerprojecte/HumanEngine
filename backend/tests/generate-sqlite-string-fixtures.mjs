/** Real Node Buffer and better-sqlite3 binding boundary; synthetic in-memory data. */
import { readFileSync, writeFileSync } from 'node:fs'
import Database from 'better-sqlite3'
if (process.version !== 'v24.22.1') throw Error('Pinned Node 24.22.1 required')
if (process.argv.slice(2).some((arg) => arg !== '--check')) throw Error('Only --check supported')
const db = new Database(':memory:')
const values = [
  '',
  'plain',
  'a\0b',
  '\ud800',
  '\udfff',
  '\ufffd',
  '\ud800\udfff',
  '\udfff\ud800',
  'x\ud800y',
  '\ud800\ud800\udfff',
  '中文',
  '<>&\u2028\u2029',
]
try {
  db.exec('CREATE TABLE keys(id TEXT PRIMARY KEY,value TEXT)')
  const cases = values.map((input, i) => {
    db.prepare('INSERT OR REPLACE INTO keys VALUES(?,?)').run(input, String(i))
    const row = db.prepare('SELECT hex(id) AS hex,value FROM keys WHERE id=?').get(input)
    return {
      input,
      units: Array.from({ length: input.length }, (_, j) => input.charCodeAt(j)),
      utf8: Buffer.from(input).toString('hex'),
      rowHex: row.hex,
      lookupValue: row.value,
      rowCount: db.prepare('SELECT count(*) n FROM keys').get().n,
    }
  })
  const output =
    JSON.stringify(
      { node: process.version, sqlite: db.prepare('SELECT sqlite_version() v').get().v, cases },
      null,
      2,
    ) + '\n'
  const target = new URL('../internal/jsjson/testdata/sqlite-strings.json', import.meta.url)
  if (process.argv.includes('--check')) {
    if (readFileSync(target, 'utf8') !== output) throw Error('SQLite string fixtures changed')
  } else writeFileSync(target, output)
  console.log(`${cases.length} real SQLite string binding cases`)
} finally {
  db.close()
}
