// Seed only the disposable package proof's stopped main volume. Usage events
// and their daily aggregate are operational counters, not authority records.
import { DatabaseSync } from 'node:sqlite'

const [file, start] = process.argv.slice(1)
const db = new DatabaseSync(file)
const midnight = Date.parse(start)
const keys = ['day', 'model_id', 'provider_id', 'api_model', 'role']
const columns = db.prepare('SELECT name FROM pragma_table_info(\'model_usage\')').all().map(({name}) => name)
const metrics = columns.filter(name => !keys.includes(name))
const event = db.prepare(`INSERT INTO model_usage_events (${columns.join(',')},at)
  VALUES (${Array(columns.length + 1).fill('?').join(',')})`)
db.exec('BEGIN IMMEDIATE')
if (db.prepare('SELECT EXISTS (SELECT 1 FROM model_usage UNION ALL SELECT 1 FROM model_usage_events UNION ALL SELECT 1 FROM model_usage_history) AS present').get().present) {
  throw new Error('usage fixture requires empty disposable counters')
}
for (const [offset, input, output] of [[-1, 900, 90], [0, 100, 11], [4500000, 230, 29], [5400000, 800, 80]]) {
  const at = new Date(midnight + offset).toISOString()
  const values = {
    day: at.slice(0, 10), model_id: 'browser-model', provider_id: 'browser-provider',
    api_model: 'browser-model', role: 'planner', calls: 1, duration_ms: 17,
    input_tokens: input, output_tokens: output, reported_input_tokens: input,
    reported_output_tokens: output, reported_usage_calls: 1, ordinary_input_tokens: input,
  }
  event.run(...columns.map(name => values[name] ?? 0), at)
}
db.exec(`INSERT INTO model_usage (${columns.join(',')})
  SELECT ${columns.map(name => keys.includes(name) ? name : `sum(${name})`).join(',')}
  FROM model_usage_events GROUP BY ${keys.join(',')}; COMMIT`)
db.close()
