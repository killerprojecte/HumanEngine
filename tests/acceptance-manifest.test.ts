import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, mkdirSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import test from 'node:test'
import { aggregateAcceptance } from '../scripts/acceptance/aggregate.ts'
import { acceptanceSourceHash, readManifest } from '../scripts/acceptance/manifest.ts'
import { validateManifest } from '../scripts/acceptance/validate.ts'
import {
  actualCasePassed,
  executeSafetyFile,
  runSafetyAcceptance,
  type Execution,
} from '../scripts/acceptance/runner.ts'

const root = process.cwd()
test('frozen acceptance binds 200 unique literal tests across independent safety mechanisms', () => {
  const manifest = readManifest(root)
  validateManifest(root, manifest)
  assert.equal(manifest.cases.length, 200)
  assert.ok(new Set(manifest.cases.map((item) => item.mechanism)).size >= 10)
  const duplicate = structuredClone(manifest)
  duplicate.cases[1] = duplicate.cases[0]!
  assert.throws(() => validateManifest(root, duplicate), /duplicate/)
  const changed = structuredClone(manifest)
  changed.cases[0]!.fixtureHash = '0'.repeat(64)
  assert.throws(() => validateManifest(root, changed), /fixture_changed/)
  const absent = structuredClone(manifest)
  absent.cases[0]!.testName = 'nonexistent test is not execution evidence'
  assert.throws(() => validateManifest(root, absent))
})

test('validate-only reports not_run and cannot authorize advancement or overwrite evidence', async () => {
  const directory = mkdtempSync(join(tmpdir(), 'acceptance-report-'))
  try {
    const output = join(directory, 'report.json')
    const report = await runSafetyAcceptance({ root, output, run: false, runId: 'static-check' })
    assert.equal(report.status, 'not_run')
    assert.equal(report.advancementAllowed, false)
    assert.ok(
      report.testResults.every((item) => item.status === 'not_run' && item.exitCode === null),
    )
    assert.deepEqual(report.executions, [])
    assert.equal(report.sourceHash, acceptanceSourceHash(root))
    assert.equal(
      JSON.parse(readFileSync(output, 'utf8')).semanticGold,
      'pending_independent_review',
    )
    await assert.rejects(runSafetyAcceptance({ root, output, run: false }))
  } finally {
    rmSync(directory, { recursive: true, force: true })
  }
})

test('acceptance accidental egress guard rejects public transport before I/O and allows synthetic loopback', () => {
  const guard = join(root, 'scripts/acceptance/network-deny.mjs')
  const script = `import http from 'node:http'; import assert from 'node:assert/strict';
    assert.throws(()=>http.get('https://example.com'),/acceptance_external_network_denied/);
    assert.throws(()=>fetch('https://example.com'),/acceptance_external_network_denied/);
    const server=http.createServer((_q,r)=>r.end('synthetic'));
    await new Promise(r=>server.listen(0,'127.0.0.1',r));
    try { const response=await fetch('http://127.0.0.1:'+server.address().port); assert.equal(await response.text(),'synthetic') } finally { server.closeAllConnections(); await new Promise(r=>server.close(r)) }`
  const result = spawnSync(
    process.execPath,
    ['--import', guard, '--input-type=module', '-e', script],
    { encoding: 'utf8', timeout: 10_000, env: { PATH: process.env.PATH } },
  )
  assert.equal(result.status, 0, result.stderr)
})

test('structured results require exactly one actual selected pass; skips, missing names and child failures block', () => {
  const item = readManifest(root).cases[0]!
  const good: Execution = {
    command: [],
    exitCode: 0,
    signal: null,
    malformed: false,
    events: [
      {
        type: 'test:pass',
        name: item.testName,
        file: join(root, item.file),
        nesting: 0,
        skip: false,
        todo: false,
      },
    ],
  }
  assert.equal(actualCasePassed(root, item, good), true)
  for (const execution of [
    { ...good, exitCode: 1 },
    { ...good, malformed: true },
    { ...good, events: [] },
    { ...good, events: [...good.events, ...good.events] },
    ...[
      { skip: true },
      { todo: true },
      { type: 'test:fail' },
      { file: 'tests/other.test.ts' },
      { nesting: 1 },
      { name: 'other' },
    ].map((change) => ({ ...good, events: [{ ...good.events[0]!, ...change }] })),
  ])
    assert.equal(actualCasePassed(root, item, execution), false)
})

test('actual subprocess reporter distinguishes literal metacharacter pass, skip, todo, fail and absent test', async () => {
  const directory = mkdtempSync(join(tmpdir(), 'acceptance-child-'))
  try {
    mkdirSync(join(directory, 'tests'))
    writeFileSync(join(directory, 'package.json'), '{"type":"module"}')
    const file = 'tests/synthetic.test.ts'
    const literalName = 'exact (A) [B] + ? .* $ ^ | {C} \\ end'
    writeFileSync(
      join(directory, file),
      `import test from 'node:test'; import assert from 'node:assert/strict';
      test(${JSON.stringify(literalName)},()=>{});
      test('skip', {skip:true},()=>{});
      test('todo', {todo:true},()=>{});
      test('fail',()=>assert.fail('synthetic'));
      test('unselected throwing test',()=>assert.fail('must not run'));`,
    )
    const base = readManifest(root).cases[0]!
    for (const [testName, expected] of [
      [literalName, true],
      ['skip', false],
      ['todo', false],
      ['fail', false],
      ['absent', false],
    ] as const) {
      const item = { ...base, file, testName }
      const execution = await executeSafetyFile(directory, [item])
      assert.equal(actualCasePassed(directory, item, execution), expected, testName)
      assert.ok(execution.command.includes('--test-name-pattern'))
      assert.ok(
        execution.events.every((event) => event.name !== 'unselected throwing test' || event.skip),
      )
    }
  } finally {
    rmSync(directory, { recursive: true, force: true })
  }
})

test('aggregate refuses static-only safety and mismatched longitudinal source receipts', () => {
  const sourceHash = acceptanceSourceHash(root)
  const safety = {
    schemaVersion: 1,
    kind: 'safety_manifest',
    status: 'not_run',
    sourceHash,
    semanticGold: 'pending_independent_review',
    runId: 'synthetic-validator-fixture',
  }
  const longitudinal = {
    schemaVersion: 1,
    kind: 'longitudinal_ablation',
    status: 'passed',
    sourceHash,
    semanticGold: 'pending_independent_review',
    runId: 'synthetic-validator-fixture',
  }
  assert.throws(() => aggregateAcceptance(root, safety, longitudinal))
  assert.throws(() =>
    aggregateAcceptance(
      root,
      { ...safety, status: 'passed' },
      { ...longitudinal, sourceHash: '0'.repeat(64) },
    ),
  )
})
