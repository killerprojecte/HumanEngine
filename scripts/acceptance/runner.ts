import { spawn } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { acceptanceSourceHash, readManifest, sha256 } from './manifest.ts'
import { validateManifest } from './validate.ts'
import type { CaseResult, SafetyCase } from './contracts.ts'

const escape = (value: string) => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
export interface Execution {
  command: string[]
  exitCode: number | null
  signal: string | null
  events: {
    type: string
    name: string
    file?: string
    nesting: number
    skip: boolean
    todo: boolean
  }[]
  malformed: boolean
}
export function actualCasePassed(root: string, item: SafetyCase, execution: Execution): boolean {
  const events = execution.events.filter(
    (event) =>
      event.name === item.testName &&
      event.nesting === 0 &&
      event.file &&
      resolve(root, event.file) === join(root, item.file),
  )
  return (
    execution.exitCode === 0 &&
    !execution.malformed &&
    events.length === 1 &&
    events[0]!.type === 'test:pass' &&
    !events[0]!.skip &&
    !events[0]!.todo
  )
}
export async function executeSafetyFile(root: string, cases: SafetyCase[]): Promise<Execution> {
  const directory = dirname(fileURLToPath(import.meta.url))
  const args = [
    '--import',
    join(directory, 'network-deny.mjs'),
    '--import',
    import.meta.resolve('tsx'),
    '--test',
    '--test-concurrency=1',
    '--test-reporter',
    join(directory, 'reporter.mjs'),
    '--test-name-pattern',
    `^(?:${cases.map((item) => escape(item.testName)).join('|')})$`,
    cases[0]!.file,
  ]
  // Do not inherit API credentials, provider endpoints or NODE_OPTIONS into the harness.
  const env: NodeJS.ProcessEnv = {
    PATH: process.env.PATH,
    HOME: process.env.HOME,
    TMPDIR: process.env.TMPDIR ?? '/tmp',
    TZ: 'UTC',
    NODE_ENV: 'test',
  }
  return await new Promise((resolveResult) => {
    const child = spawn(process.execPath, args, {
      cwd: root,
      env,
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    const events: Execution['events'] = []
    let buffer = '',
      malformed = false,
      bytes = 0
    let killTimer: ReturnType<typeof setTimeout> | undefined
    const timer = setTimeout(() => {
      malformed = true
      child.kill('SIGTERM')
      killTimer = setTimeout(() => child.kill('SIGKILL'), 2_000)
    }, 120_000)
    const processLine = (line: string) => {
      if (!line.trim()) return
      try {
        const value = JSON.parse(line)
        if (
          !['test:pass', 'test:fail'].includes(value.type) ||
          typeof value.name !== 'string' ||
          typeof value.nesting !== 'number'
        )
          throw new Error('event')
        events.push(value)
      } catch {
        malformed = true
      }
    }
    child.stdout.on('data', (chunk: Buffer) => {
      bytes += chunk.length
      if (bytes > 8 * 1024 * 1024) {
        malformed = true
        child.kill('SIGTERM')
        return
      }
      buffer += chunk.toString('utf8')
      let end: number
      while ((end = buffer.indexOf('\n')) >= 0) {
        processLine(buffer.slice(0, end))
        buffer = buffer.slice(end + 1)
      }
    })
    // Consume but never persist arbitrary diagnostic content, which may contain fixture text.
    child.stderr.on('data', () => {})
    child.on('error', () => {
      malformed = true
    })
    child.on('close', (exitCode, signal) => {
      clearTimeout(timer)
      if (killTimer) clearTimeout(killTimer)
      processLine(buffer)
      resolveResult({ command: [process.execPath, ...args], exitCode, signal, events, malformed })
    })
  })
}

export async function runSafetyAcceptance(options: {
  root: string
  output: string
  run: boolean
  caseIds?: readonly string[]
  runId?: string
}) {
  const root = resolve(options.root)
  const runId = options.runId ?? `safety-${new Date().toISOString().replace(/[^0-9T]/g, '')}`
  if (!/^[A-Za-z0-9_-]{1,100}$/.test(runId)) throw new Error('acceptance_run_id')
  const startedAt = new Date().toISOString()
  const manifest = readManifest(root)
  validateManifest(root, manifest)
  const sourceHash = acceptanceSourceHash(root)
  const manifestHash = sha256(readFileSync(join(root, 'docs/acceptance/safety-manifest.json')))
  const runnerHash = sha256(
    ['contracts.ts', 'manifest.ts', 'validate.ts', 'runner.ts', 'reporter.mjs', 'network-deny.mjs']
      .map(
        (file) =>
          `${file}\0${sha256(readFileSync(join(dirname(fileURLToPath(import.meta.url)), file)))}`,
      )
      .join('\n'),
  )
  const requested = options.caseIds ? new Set(options.caseIds) : undefined
  if (
    requested &&
    (requested.size !== options.caseIds!.length ||
      [...requested].some((id) => !manifest.cases.some((item) => item.id === id)))
  )
    throw new Error('acceptance_unknown_case')
  const selected = manifest.cases.filter((item) => !requested || requested.has(item.id))
  if (!selected.length) throw new Error('acceptance_empty_selection')
  const results: CaseResult[] = selected.map((item) => ({
    caseId: item.id,
    file: item.file,
    testName: item.testName,
    status: 'not_run',
    exitCode: null,
  }))
  const executions: {
    command: string[]
    file: string
    selectedCount: number
    exitCode: number | null
    signal: string | null
    malformed: boolean
  }[] = []
  if (options.run) {
    for (const file of [...new Set(selected.map((item) => item.file))]) {
      const cases = selected.filter((item) => item.file === file)
      const execution = await executeSafetyFile(root, cases)
      executions.push({
        command: execution.command,
        file,
        selectedCount: cases.length,
        exitCode: execution.exitCode,
        signal: execution.signal,
        malformed: execution.malformed,
      })
      for (const item of cases) {
        const result = results.find((value) => value.caseId === item.id)!
        const passed = actualCasePassed(root, item, execution)
        Object.assign(result, {
          exitCode: execution.exitCode,
          status: passed ? 'passed' : 'failed',
          ...(!passed ? { reason: 'actual_test_missing_skipped_failed_or_process_failed' } : {}),
        })
      }
      if (results.some((item) => item.status === 'failed')) break
    }
  }
  const sourceUnchanged = sourceHash === acceptanceSourceHash(root)
  const completeManifest = selected.length === manifest.cases.length
  const status =
    !sourceUnchanged || results.some((item) => item.status === 'failed')
      ? 'failed'
      : options.run && results.every((item) => item.status === 'passed')
        ? 'passed'
        : 'not_run'
  const report = {
    schemaVersion: 1,
    kind: 'safety_manifest',
    runId,
    sourceHash,
    manifestHash,
    runnerHash,
    startedAt,
    finishedAt: new Date().toISOString(),
    status,
    completeManifest,
    advancementAllowed: status === 'passed' && completeManifest && sourceUnchanged,
    sourceUnchanged,
    selectedCaseIds: selected.map((item) => item.id),
    testResults: results,
    executions,
    networkPolicy: 'loopback_only_accidental_egress_guard_not_plugin_sandbox',
    semanticGold: 'pending_independent_review',
  }
  mkdirSync(dirname(resolve(options.output)), { recursive: true })
  writeFileSync(options.output, JSON.stringify(report, null, 2) + '\n', { flag: 'wx' })
  return report
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2)
  const value = (flag: string) => {
    const index = args.indexOf(flag)
    return index < 0 ? undefined : args[index + 1]
  }
  const known = new Set(['--run', '--root', '--output', '--case', '--run-id'])
  for (let index = 0; index < args.length; index++) {
    if (!known.has(args[index]!)) throw new Error('acceptance_argument')
    if (args[index] !== '--run') index++
  }
  const output = value('--output')
  if (!output) throw new Error('acceptance_output_required')
  runSafetyAcceptance({
    root: value('--root') ?? process.cwd(),
    output,
    run: args.includes('--run'),
    caseIds: value('--case')?.split(','),
    runId: value('--run-id'),
  })
    .then((report) => {
      process.stdout.write(
        JSON.stringify({
          status: report.status,
          advancementAllowed: report.advancementAllowed,
          cases: report.testResults.length,
        }) + '\n',
      )
      if (report.status === 'failed') process.exitCode = 1
    })
    .catch(() => {
      process.stderr.write('acceptance_failed_closed\n')
      process.exitCode = 1
    })
}
