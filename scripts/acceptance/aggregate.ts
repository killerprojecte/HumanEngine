import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { acceptanceSourceHash, readManifest, sha256 } from './manifest.ts'
import { validateManifest } from './validate.ts'

const variants = ['no-memory', 'ordinary', 'qualified-feature'] as const
const stages = [
  'initial-learning',
  'qualified-read',
  'source-edit-independent-or',
  'explicit-correction',
  'multiperson-plan-amendment',
  'source-withdrawal',
  'restart-no-replay',
  'read-feature-off',
] as const
const record = (value: unknown): Record<string, unknown> => {
  if (!value || typeof value !== 'object' || Array.isArray(value))
    throw new Error('acceptance_report_shape')
  return value as Record<string, unknown>
}
const array = (value: unknown): unknown[] => {
  if (!Array.isArray(value)) throw new Error('acceptance_report_array')
  return value
}
const requireTrue = (value: unknown) => {
  if (value !== true) throw new Error('acceptance_report_not_passed')
}

/** Merge only trusted local run receipts, never substitute for owner type/build/UI/publication gates. */
export function aggregateAcceptance(
  root: string,
  safetyInput: unknown,
  longitudinalInput: unknown,
) {
  const manifest = readManifest(root)
  validateManifest(root, manifest)
  const sourceHash = acceptanceSourceHash(root)
  const safety = record(safetyInput),
    longitudinal = record(longitudinalInput)
  for (const report of [safety, longitudinal]) {
    if (
      report.schemaVersion !== 1 ||
      report.status !== 'passed' ||
      report.sourceHash !== sourceHash ||
      report.semanticGold !== 'pending_independent_review' ||
      typeof report.runId !== 'string'
    )
      throw new Error('acceptance_report_incompatible')
  }
  if (safety.kind !== 'safety_manifest' || longitudinal.kind !== 'longitudinal_ablation')
    throw new Error('acceptance_report_kind')
  requireTrue(safety.completeManifest)
  requireTrue(safety.advancementAllowed)
  requireTrue(safety.sourceUnchanged)
  const cases = array(safety.testResults).map(record)
  if (cases.length !== manifest.cases.length) throw new Error('acceptance_report_case_count')
  for (const item of manifest.cases) {
    const matches = cases.filter((entry) => entry.caseId === item.id)
    if (
      matches.length !== 1 ||
      matches[0]!.file !== item.file ||
      matches[0]!.testName !== item.testName ||
      matches[0]!.status !== 'passed' ||
      matches[0]!.exitCode !== 0
    )
      throw new Error('acceptance_report_case_failed')
  }
  const actualVariants = array(longitudinal.variants).map(record)
  if (actualVariants.length !== variants.length) throw new Error('acceptance_ablation_count')
  const configurations = new Set<string>()
  for (const id of variants) {
    const matching = actualVariants.filter((variant) => variant.id === id)
    if (matching.length !== 1) throw new Error('acceptance_ablation_missing')
    const variant = matching[0]!
    if (array(variant.mechanicalFailures).length) throw new Error('acceptance_ablation_failure')
    const configuration = record(variant.configuration)
    configurations.add(
      JSON.stringify(
        Object.keys(configuration)
          .sort()
          .map((key) => [key, configuration[key]]),
      ),
    )
    const actualStages = array(variant.stages).map(record)
    if (actualStages.length !== stages.length)
      throw new Error('acceptance_longitudinal_stage_count')
    let learningCalls = 0
    for (const stageId of stages) {
      const found = actualStages.filter((stage) => stage.id === stageId)
      if (found.length !== 1) throw new Error('acceptance_longitudinal_stage_missing')
      const stage = found[0]!
      const outcomes = array(stage.outcomes).map(record)
      if (!outcomes.length) throw new Error('acceptance_longitudinal_no_checks')
      for (const outcome of outcomes) requireTrue(outcome.passed)
      const calls = record(stage.calls)
      if (!Number.isSafeInteger(calls.learning) || (calls.learning as number) < 0)
        throw new Error('acceptance_longitudinal_calls')
      learningCalls += calls.learning as number
      array(stage.selectedEvidence)
      array(stage.coverageReasons)
    }
    if (id === 'no-memory' ? learningCalls !== 0 : learningCalls <= 0)
      throw new Error('acceptance_ablation_not_executed')
  }
  if (configurations.size !== variants.length) throw new Error('acceptance_ablation_relabelled')
  return {
    schemaVersion: 1,
    kind: 'integrated_mechanism_acceptance',
    status: 'passed',
    sourceHash,
    safetyRunId: safety.runId,
    longitudinalRunId: longitudinal.runId,
    cases: cases.length,
    variants: [...variants],
    semanticGold: 'pending_independent_review',
    scope: 'synthetic_safety_and_actual_wiring_not_semantic_benefit',
    remainingGates: [
      'owner_aggregate',
      'types',
      'format',
      'build',
      'ui',
      'clean_package',
      'publication_authorization_and_remote_verification',
    ],
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2)
  const [safetyPath, longitudinalPath, output] = args
  if (args.length !== 3 || !safetyPath || !longitudinalPath || !output)
    throw new Error('acceptance_aggregate_arguments')
  try {
    const safetyBytes = readFileSync(safetyPath),
      longitudinalBytes = readFileSync(longitudinalPath)
    const report = {
      ...aggregateAcceptance(
        process.cwd(),
        JSON.parse(safetyBytes.toString()),
        JSON.parse(longitudinalBytes.toString()),
      ),
      safetyReportHash: sha256(safetyBytes),
      longitudinalReportHash: sha256(longitudinalBytes),
      recordedAt: new Date().toISOString(),
    }
    mkdirSync(dirname(resolve(output)), { recursive: true })
    writeFileSync(output, JSON.stringify(report, null, 2) + '\n', { flag: 'wx' })
    process.stdout.write('integrated_mechanism_acceptance_passed\n')
  } catch {
    process.stderr.write('integrated_mechanism_acceptance_failed_closed\n')
    process.exitCode = 1
  }
}
