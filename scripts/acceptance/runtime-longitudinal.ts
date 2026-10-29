import { createHash, randomUUID } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { acceptanceSourceHash } from './manifest.ts'
import {
  longitudinalHarness,
  type LongitudinalCounters,
  type SelectedEvidence,
} from './runtime-longitudinal-harness.ts'
import {
  longitudinalEvents as events,
  longitudinalFacts as facts,
  longitudinalVariants,
  longitudinalFixtureVersion,
  longitudinalFixtureHash,
  type LongitudinalVariant,
} from './runtime-longitudinal-fixture.ts'

export const longitudinalRunnerVersion = 'runtime-longitudinal-runner-v1'
export const longitudinalStageIds = [
  'initial-learning',
  'qualified-read',
  'source-edit-independent-or',
  'explicit-correction',
  'multiperson-plan-amendment',
  'source-withdrawal',
  'restart-no-replay',
  'read-feature-off',
] as const
export interface StageReceipt {
  id: (typeof longitudinalStageIds)[number]
  eventDates: string[]
  calls: LongitudinalCounters
  selectedEvidence: SelectedEvidence[]
  outcomes: Array<{ id: string; passed: boolean }>
  coverageReasons: string[]
}
export interface VariantReceipt {
  id: LongitudinalVariant['id']
  configuration: {
    memoryReadEnabled: boolean
    learningEnabled: boolean
    memoryCommitmentsEnabled: boolean
    memoryCommitmentsAnswerEnabled: boolean
    memoryHistoricalReadEnabled: boolean
    sourceEventRevisionsEnabled: true
  }
  callsByRole: LongitudinalCounters
  stages: StageReceipt[]
  mechanicalFailures: string[]
  clock: {
    billingDayAtStart: string
    billingDayAtEnd: string
    receivedAtPolicy: 'actual_runtime_wall_clock'
    eventDatesAreSynthetic: true
  }
}
export interface LongitudinalReport {
  schemaVersion: 1
  kind: 'longitudinal_ablation'
  runId: string
  sourceHash: string
  fixtureVersion: string
  fixtureHash: string
  runnerVersion: string
  runnerHash: string
  startedAt: string
  finishedAt: string
  status: 'passed' | 'failed'
  semanticGold: 'pending_independent_review'
  variants: VariantReceipt[]
  limitations: string[]
}
const delta = (after: LongitudinalCounters, before: LongitudinalCounters): LongitudinalCounters =>
  Object.fromEntries(
    Object.keys(after).map((key) => [
      key,
      after[key as keyof LongitudinalCounters] - before[key as keyof LongitudinalCounters],
    ]),
  ) as unknown as LongitudinalCounters
const event = (id: string) => events.find((item) => item.id === id)!
async function runVariant(variant: LongitudinalVariant): Promise<VariantReceipt> {
  const h = longitudinalHarness(variant)
  const result: VariantReceipt = {
    id: variant.id,
    configuration: {
      memoryReadEnabled: variant.memory,
      learningEnabled: variant.memory,
      memoryCommitmentsEnabled: variant.qualified,
      memoryCommitmentsAnswerEnabled: variant.qualified,
      memoryHistoricalReadEnabled: variant.qualified,
      sourceEventRevisionsEnabled: true,
    },
    callsByRole: { ...h.calls },
    stages: [],
    mechanicalFailures: [],
    clock: {
      billingDayAtStart: h.billingDay(),
      billingDayAtEnd: h.billingDay(),
      receivedAtPolicy: 'actual_runtime_wall_clock',
      eventDatesAreSynthetic: true,
    },
  }
  let current: StageReceipt | undefined
  const stage = async (
    id: StageReceipt['id'],
    inputIds: string[],
    run: (
      check: (condition: boolean, name: string) => void,
      receipt: StageReceipt,
    ) => Promise<void>,
  ) => {
    const before = { ...h.calls }
    current = {
      id,
      eventDates: [...new Set(inputIds.map((id) => event(id).eventDate))],
      calls: delta(before, before),
      selectedEvidence: [],
      outcomes: [],
      coverageReasons: [],
    }
    result.stages.push(current)
    await run((passed, name) => {
      current!.outcomes.push({ id: name, passed })
      if (!passed) result.mechanicalFailures.push(`${id}:${name}`)
    }, current)
    current.calls = delta(h.calls, before)
  }
  try {
    await stage(
      'initial-learning',
      ['alice-original', 'alice-independent', 'bob-independent'],
      async (check) => {
        await h.learn(events.slice(0, 3))
        const records = h.records(),
          coffee = records.find((record) => record.content === facts.coffee)
        check(
          variant.memory ? records.length === 2 : records.length === 0,
          'actual_learning_flag_changes_persistence',
        )
        check(
          variant.memory ? coffee?.sources.length === 2 : h.calls.learning === 0,
          'independent_or_proofs_or_zero_learning',
        )
        check(
          !variant.memory ||
            (coffee?.scope.kind === 'scene' &&
              coffee.scope.personId === 'alice' &&
              records.some(
                (r) =>
                  r.content === facts.bob && r.scope.kind === 'scene' && r.scope.personId === 'bob',
              )),
          'same_nickname_does_not_merge_people',
        )
        check(
          [result.clock.billingDayAtStart, h.billingDay()].includes(
            h.originals.get('alice-original')!.receivedAt!.slice(0, 10),
          ),
          'ingress_receipt_uses_execution_clock_not_declared_event_day',
        )
      },
    )
    await h.reopen()
    await stage(
      'qualified-read',
      ['alice-original', 'alice-independent'],
      async (check, receipt) => {
        check(
          h.engine.runtime.preview(h.scene.id).length === 0,
          'old_chat_not_replayed_as_current_query_context',
        )
        const read = await h.query('person')
        receipt.selectedEvidence = read.evidence
        receipt.coverageReasons = read.coverageReasons
        const contents = read.evidence.flatMap((item) => item.memoryContents)
        check(
          variant.memory ? contents.includes(facts.coffee) : contents.length === 0,
          'actual_final_handoff_obeys_memory_ablation',
        )
        check(
          !variant.qualified || !contents.includes(facts.bob),
          'qualified_person_read_remains_exact_person',
        )
        check(read.bytes > 0, 'actual_serialized_replyer_request_observed')
        if (!variant.memory)
          receipt.coverageReasons.push('memory_read_disabled_no_retained_evidence')
      },
    )
    await stage('source-edit-independent-or', ['alice-original'], async (check) => {
      check(h.editOriginal().status === 'edited', 'explicit_ordered_edit_committed')
      const coffee = h.records().find((record) => record.content === facts.coffee)
      check(
        variant.memory
          ? coffee?.sources.length === 1 &&
              coffee.sources[0]!.messageId === h.originals.get('alice-independent')!.id
          : !coffee,
        'edited_source_removed_independent_alternative_survives',
      )
    })
    await stage('explicit-correction', ['alice-correction'], async (check, receipt) => {
      await h.learn([event('alice-correction')])
      await h.reopen()
      const read = await h.query('person')
      receipt.selectedEvidence = read.evidence
      const contents = h.records().map((record) => record.content)
      check(
        variant.memory
          ? contents.includes(facts.tea) && !contents.includes(facts.coffee)
          : contents.length === 0,
        'source_only_correction_reconciles_without_borrowing_content',
      )
      check(!variant.memory || contents.includes(facts.bob), 'unrelated_person_preserved')
    })
    await stage(
      'multiperson-plan-amendment',
      ['plan-18', 'confirm-18', 'amend-19', 'confirm-19'],
      async (check, receipt) => {
        await h.learn([event('plan-18')])
        await h.command(event('confirm-18'))
        if (variant.qualified)
          check(
            h.plans().some((view) => view.ref.version === 1 && view.confirmation === 'confirmed'),
            'two_people_confirm_same_18_version',
          )
        await h.command(event('amend-19'))
        if (variant.qualified)
          check(
            h.plans().some((view) => view.ref.version === 1 && view.confirmation === 'confirmed') &&
              h.plans().some((view) => view.ref.version === 2 && view.confirmation === 'proposed'),
            'one_person_amendment_does_not_erase_old_confirmation',
          )
        await h.command(event('confirm-19'))
        await h.reopen()
        const read = await h.query('plan')
        receipt.selectedEvidence = read.evidence
        receipt.coverageReasons = read.coverageReasons
        if (variant.qualified) {
          check(
            h.plans().length === 1 &&
              h.plans()[0]!.ref.version === 2 &&
              h.plans()[0]!.confirmation === 'confirmed',
            'second_person_confirms_exact_19_version',
          )
          const selected = read.evidence.filter((item) => item.stateKinds.includes('commitment'))
          check(
            selected.length === 1 &&
              selected[0]!.qualifications.every((q) => q === 'declared_not_verified'),
            'final_whole_page_keeps_declaration_not_verified_qualification',
          )
          check(
            read.evidence.some((item) => item.stateKinds.includes('commitment_timeline')),
            'recorded_timeline_selected_with_current_state',
          )
          check(
            h.plans().every((view) => view.terms?.occurredAt === undefined),
            'event_metadata_does_not_fabricate_plan_occurrence_date',
          )
        } else {
          check(
            h.plans().length === 0 && read.evidence.every((item) => item.stateKinds.length === 0),
            'baseline_never_inherits_structural_confirmation',
          )
          receipt.coverageReasons.push(
            variant.memory
              ? 'structured_tracking_disabled_plain_declarations_only'
              : 'memory_disabled_no_plan_state',
          )
        }
      },
    )
    await stage('source-withdrawal', ['alice-correction', 'confirm-19'], async (check, receipt) => {
      h.recall('alice-correction')
      h.recall('confirm-19')
      check(
        !h.records().some((record) => record.content === facts.tea),
        'withdrawn_preference_not_current',
      )
      check(
        !h.records().some((record) => record.content === facts.coffee),
        'superseded_preference_not_resurrected',
      )
      check(
        !variant.memory || h.records().some((record) => record.content === facts.bob),
        'lawful_other_person_survives',
      )
      const retained = JSON.stringify(
        h.store.db.prepare('SELECT value FROM memory_assertion_revisions').all(),
      )
      check(
        !retained.includes(facts.tea),
        'withdrawn_assertion_body_physically_absent_from_history',
      )
      if (variant.qualified)
        check(
          h.plans().length === 1 &&
            h.plans()[0]!.ref.version === 2 &&
            h.plans()[0]!.confirmation === 'incomplete',
          'withdrawn_19_confirmation_never_falls_back_to_18',
        )
      const read = await h.query('plan')
      receipt.selectedEvidence = read.evidence
      receipt.coverageReasons = read.coverageReasons
      if (variant.qualified) {
        const current = read.evidence.find((item) => item.stateKinds.includes('commitment'))
        check(
          current?.confirmations.length === 1 && current.confirmations[0] === 'incomplete',
          'actual_replyer_current_state_stays_incomplete',
        )
        check(
          read.coverageReasons.includes('privacy_or_proof_unavailable'),
          'actual_history_handoff_marks_redacted_evidence_unknown',
        )
      }
    })
    await stage('restart-no-replay', [], async (check) => {
      const before = { ...h.calls }
      await h.reopen()
      check(JSON.stringify(h.calls) === JSON.stringify(before), 'restart_zero_model_calls')
      check(
        h.engine.runtime.preview(h.scene.id).length === 0 && h.engine.learningStatus().queued === 0,
        'restart_has_no_old_chat_or_learning_backlog',
      )
      check(
        !h.records().some((record) => record.content === facts.tea),
        'privacy_state_stays_absent_after_reopen',
      )
      if (variant.qualified)
        check(h.plans()[0]?.confirmation === 'incomplete', 'incomplete_plan_persists')
    })
    await stage('read-feature-off', [], async (check, receipt) => {
      const retained = h.engine.memory.adminList().length
      h.readEnabled(false)
      h.qualifiedEnabled(false)
      const read = await h.query('person', true)
      receipt.selectedEvidence = read.evidence
      receipt.coverageReasons = [
        'memory_read_disabled',
        'structured_features_disabled',
        'admin_storage_not_deleted',
      ]
      check(
        read.evidence.every(
          (item) => item.memoryContents.length === 0 && item.stateKinds.length === 0,
        ),
        'off_final_handoff_has_no_retained_memory',
      )
      check(
        h.engine.memory.adminList().length === retained,
        'read_off_does_not_erase_admin_storage',
      )
      check(h.plans().length === 0, 'qualified_feature_off_removes_model_plan_eligibility')
    })
  } catch (error) {
    const code =
      error instanceof Error && /^longitudinal_[a-z_]+$/.test(error.message)
        ? error.message
        : 'unexpected_runtime_failure'
    result.mechanicalFailures.push(`${current?.id ?? 'setup'}:${code}`)
    current?.outcomes.push({ id: code, passed: false })
  } finally {
    result.callsByRole = { ...h.calls }
    result.clock.billingDayAtEnd = h.billingDay()
    await h.close()
  }
  return result
}
/** No real provider, credentials, endpoint or price overrides are accepted. */
export async function runRuntimeLongitudinal(
  options: { runId?: string } = {},
): Promise<LongitudinalReport> {
  if (
    Object.keys(options).some((key) => key !== 'runId') ||
    (options.runId !== undefined && !/^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$/.test(options.runId))
  )
    throw Error('longitudinal_invalid_options')
  const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
  const startedAt = new Date().toISOString(),
    sourceHash = acceptanceSourceHash(root)
  const variants: VariantReceipt[] = []
  for (const variant of longitudinalVariants) variants.push(await runVariant(variant))
  if (acceptanceSourceHash(root) !== sourceHash)
    for (const variant of variants)
      variant.mechanicalFailures.push('source_changed_during_acceptance')
  const runnerHash = createHash('sha256')
    .update(
      ['runtime-longitudinal.ts', 'runtime-longitudinal-harness.ts']
        .map((file) => readFileSync(new URL(file, import.meta.url)))
        .reduce((acc, buffer) => Buffer.concat([acc, buffer]), Buffer.alloc(0)),
    )
    .digest('hex')
  return {
    schemaVersion: 1,
    kind: 'longitudinal_ablation',
    runId: options.runId ?? randomUUID(),
    sourceHash,
    fixtureVersion: longitudinalFixtureVersion,
    fixtureHash: longitudinalFixtureHash,
    runnerVersion: longitudinalRunnerVersion,
    runnerHash,
    startedAt,
    finishedAt: new Date().toISOString(),
    status: variants.every(
      (variant) =>
        variant.mechanicalFailures.length === 0 &&
        variant.stages.length === longitudinalStageIds.length,
    )
      ? 'passed'
      : 'failed',
    semanticGold: 'pending_independent_review',
    variants,
    limitations: [
      'Synthetic scripts prove mechanism contracts, not semantic extraction accuracy or user benefit.',
      'Declared event dates span days; this execution does not simulate elapsed real days or reset billing time.',
      'No provider network is configured; usage values are synthetic, not measured tokens or paid costs.',
      'Final evidence is observed at the actual Replyer input; no answer text is scored and no platform delivery is requested.',
      'This finite vertical scenario complements, not replaces, the frozen safety manifest and production gates.',
    ],
  }
}
