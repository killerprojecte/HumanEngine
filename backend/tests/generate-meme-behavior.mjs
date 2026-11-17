/** P1-B prompt delta from the published legacy source, not a frozen-oracle rewrite. */
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
const root = resolve(process.argv[2] ?? '..')
const runtime = readFileSync(resolve(root, 'src/runtime.ts'), 'utf8')
const engine = readFileSync(resolve(root, 'src/engine.ts'), 'utf8')
const between = (text, start, end) => {
  const begin = text.indexOf(start),
    finish = text.indexOf(end, begin)
  if (begin < 0 || finish < 0 || text.indexOf(start, begin + 1) !== -1)
    throw Error('ambiguous source contract')
  return text.slice(begin, finish)
}
const sourceGitBlobs = {
  'src/runtime.ts': 'f2f78ca47ba008b8ceac61d7228b4f7d8d98e57f',
  'src/engine.ts': 'fbc853fc2dc9f58618eb724384eda5928a12febb',
}
for (const [path, expected] of Object.entries(sourceGitBlobs)) {
  const actual = execFileSync('git', ['hash-object', resolve(root, path)], {
    encoding: 'utf8',
  }).trim()
  if (actual !== expected) throw Error('source is not the tested b7f3f92 contract: ' + path)
}
const result = {
  behaviorCommit: '04b1817af8055d58021c85e94d1f72be914125d6',
  testedSourceCommit: 'b7f3f92b0d27168955f507535e38c00ed15c6826',
  sourceGitBlobs,
  sourceHashes: Object.fromEntries(
    [
      ['src/runtime.ts', runtime],
      ['src/engine.ts', engine],
    ].map(([p, t]) => [p, createHash('sha256').update(t).digest('hex')]),
  ),
  planner: between(runtime, 'When a meme would naturally express', 'A noReply plan may'),
  replyer: between(
    runtime,
    'Express the selected plan with meaningful text',
    'Proposed actions are not successful:',
  ),
  toolDescription: between(engine, 'Search analyzed available memes by intent', "',"),
}
writeFileSync(
  'internal/botruntime/testdata/meme-behavior.json',
  JSON.stringify(result, null, 2) + '\n',
)
