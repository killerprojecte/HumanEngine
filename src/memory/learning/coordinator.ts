import { withBudgetContext } from '../../budget/native-attempt.ts'
import { withModelPriority, withOverdueLearning } from '../../scheduler.ts'
import { LearningAdmission, type LearningBatch } from './admission.ts'
import { randomUUID } from 'node:crypto'
import type { Store } from '../../store.ts'
import type { RuntimeMessage } from '../../runtime.ts'
import { ModelError } from '../../models.ts'
import { providerAttemptsHandled } from '../../models/request-policy.ts'
import { startFlow, withFlowContext, currentFlowContext } from '../../diagnostics/flow.ts'
import { modelFailureReason } from '../../diagnostics/model-flow.ts'
import { sanitizeError } from '../../diagnostics/errors.ts'
import { runLearningBatch } from './batch-runner.ts'
import type { LearningPorts, CommitProgress } from './batch-contracts.ts'
export class LearningCoordinator {
  private readonly learningInputTraces = new Map<string, string>()
  private readonly learningBatches = new Map<string, RuntimeMessage[]>()
  private readonly learningTimers = new Map<string, ReturnType<typeof setTimeout>>()
  private readonly learningTimerDue = new Map<string, number>()
  private readonly learningTasks = new Map<string, AbortController>()
  private readonly learningRetries = new Map<
    string,
    {
      messages: RuntimeMessage[]
      attempt: number
      due: number
      batchId?: string
      parentTraceId?: string
      linkedTraceIds?: string[]
    }
  >()
  private readonly learningActiveMessages = new Map<string, RuntimeMessage[]>()
  private readonly learningSurvivors = new Map<string, RuntimeMessage[]>()
  private readonly learningFailures = new Map<
    string,
    {
      id: string
      sceneId: string
      attempt: number
      messageCount: number
      partial: boolean
      canRetry: boolean
      createdAt: string
      messages: RuntimeMessage[]
    }
  >()
  private readonly learningCounts = {
    accepted: 0,
    completed: 0,
    failed: 0,
    overflow: 0,
    retried: 0,
  }
  private overdueLearningScene?: string
  private disposed = false
  private readonly admission: LearningAdmission
  constructor(
    private readonly store: Store,
    private readonly ports: LearningPorts,
  ) {
    const settings = store.settings
    this.admission = new LearningAdmission({
      quietMs: settings.learningBatchMs,
      maxWaitMs: Math.max(settings.learningBatchMs, settings.learningMaxWaitMs),
      minIntervalMs: settings.learningMinIntervalMs,
      maxSourcesPerScene: settings.learningQueueMaxMessages,
      maxSourcesTotal: settings.learningQueueMaxTotalMessages,
    })
  }
  settingsChanged() {
    const settings = this.store.settings
    this.admission.configure({
      quietMs: settings.learningBatchMs,
      maxWaitMs: Math.max(settings.learningBatchMs, settings.learningMaxWaitMs),
      minIntervalMs: settings.learningMinIntervalMs,
      maxSourcesPerScene: settings.learningQueueMaxMessages,
      maxSourcesTotal: settings.learningQueueMaxTotalMessages,
    })
    for (const sceneId of this.learningBatches.keys()) this.scheduleLearning(sceneId)
  }
  dispose() {
    this.disposed = true
    this.admission.setEnabled(false)
    this.learningInputTraces.clear()
    this.learningBatches.clear()
    this.learningRetries.clear()
    this.learningFailures.clear()
    this.learningSurvivors.clear()
    for (const timer of this.learningTimers.values()) clearTimeout(timer)
    this.learningTimerDue.clear()
    for (const controller of this.learningTasks.values()) controller.abort()
  }
  recall(sceneId: string, messageId: string) {
    this.admission.recall(sceneId, messageId)
    this.learningInputTraces.delete(`${sceneId}|${messageId}`)
    this.learningBatches.set(
      sceneId,
      (this.learningBatches.get(sceneId) ?? []).filter((m) => m.id !== messageId),
    )
    for (const failure of this.learningFailures.values())
      if (failure.sceneId === sceneId)
        failure.messages = failure.messages.filter((message) => message.id !== messageId)
    const retry = this.learningRetries.get(sceneId)
    if (retry) retry.messages = retry.messages.filter((message) => message.id !== messageId)
    // An edit cancels this source without a recall tombstone. Stop treating the old
    // revision as in-flight admission, so its replacement can enter the pending queue.
    const active = this.learningSurvivors.get(sceneId)
    if (active)
      this.learningSurvivors.set(
        sceneId,
        active.filter((message) => message.id !== messageId),
      )
    this.learningTasks.get(sceneId)?.abort('source_recalled')
  }
  cancelLearning(sceneId: string) {
    this.admission.cancelScene(sceneId)
    // AbortSignal keeps its first reason. A prior source cancellation must not
    // revive survivors after a later scene-wide cancellation or OFF/ON transition.
    this.learningSurvivors.delete(sceneId)
    this.learningBatches.delete(sceneId)
    for (const key of this.learningInputTraces.keys())
      if (key.startsWith(`${sceneId}|`)) this.learningInputTraces.delete(key)
    this.learningRetries.delete(sceneId)
    for (const [id, failure] of this.learningFailures)
      if (failure.sceneId === sceneId) this.learningFailures.delete(id)
    clearTimeout(this.learningTimers.get(sceneId))
    this.learningTimers.delete(sceneId)
    this.learningTimerDue.delete(sceneId)
    this.learningTasks.get(sceneId)?.abort('learning_disabled')
  }
  learningStatus() {
    return {
      ...this.learningCounts,
      paused: [...this.learningFailures.values()].reduce(
        (sum, batch) => sum + batch.messages.length,
        0,
      ),
      failures: [...this.learningFailures.values()].map(
        ({ messages: _messages, ...metadata }) => metadata,
      ),
      queued: [...this.learningBatches.values()].reduce((sum, batch) => sum + batch.length, 0),
      retrying: [...this.learningRetries.values()].reduce(
        (sum, batch) => sum + batch.messages.length,
        0,
      ),
      active: [...this.learningActiveMessages.values()].reduce(
        (sum, batch) => sum + batch.length,
        0,
      ),
    }
  }
  retryLearning(id: string) {
    const failure = this.learningFailures.get(id)
    if (
      !failure ||
      !failure.canRetry ||
      this.disposed ||
      !this.store.scene(failure.sceneId)?.learningEnabled
    )
      return { ok: false }
    const eligible = failure.messages.filter(
      (message) =>
        message.status === 'available' && !this.store.recalled(message.sceneId, message.id),
    )
    this.learningFailures.delete(id)
    if (eligible.length) {
      for (const message of eligible) this.admission.admit(failure.sceneId, message.id)
      this.learningBatches.set(failure.sceneId, [
        ...eligible,
        ...(this.learningBatches.get(failure.sceneId) ?? []),
      ])
      this.scheduleLearning(failure.sceneId)
    }
    return { ok: true }
  }
  discardLearning(id: string) {
    const failure = this.learningFailures.get(id)
    if (!failure) return { ok: false }
    this.learningFailures.delete(id)
    this.store.log('info', 'learning', 'learning_failure_discarded', { sceneId: failure.sceneId })
    return { ok: true }
  }
  enqueueLearning(message: RuntimeMessage) {
    const sceneId = message.sceneId
    const admission = startFlow(this.store, 'learning.admission', {
      sceneId,
      counts: { inputCount: 1 },
    })
    const rejected =
      message.senderId === 'bot'
        ? 'bot_source'
        : this.disposed
          ? 'runtime_stopped'
          : !this.store.scene(sceneId)?.learningEnabled
            ? 'learning_disabled'
            : message.origin !== 'live'
              ? 'not_live_source'
              : message.status !== 'available' || this.store.recalled(sceneId, message.id)
                ? 'source_unavailable'
                : undefined
    if (rejected) {
      admission.finish('skipped', { reason: rejected, nextStep: 'none' })
      return
    }
    const batch = this.learningBatches.get(sceneId) ?? []
    const survivors = this.learningSurvivors.get(sceneId) ?? []
    const existing = [
      ...batch,
      ...(this.learningRetries.get(sceneId)?.messages ?? []),
      ...survivors,
      ...[...this.learningFailures.values()]
        .filter((failure) => failure.sceneId === sceneId)
        .flatMap((failure) => failure.messages),
    ]
    if (existing.some((item) => item.id === message.id)) {
      admission.finish('skipped', { reason: 'duplicate_source', nextStep: 'existing_batch' })
      return
    }
    const status = this.learningStatus()
    // Cancelled revisions still occupy resources until their provider settles.
    const sceneDepth =
      existing.length - survivors.length + (this.learningActiveMessages.get(sceneId)?.length ?? 0)
    if (
      sceneDepth >= this.store.settings.learningQueueMaxMessages ||
      status.queued + status.retrying + status.active + status.paused >=
        this.store.settings.learningQueueMaxTotalMessages
    ) {
      this.learningCounts.overflow++
      admission.finish('skipped', {
        reason: 'learning_queue_full',
        counts: { queueDepth: sceneDepth },
        nextStep: 'inspect_learning_queue',
      })
      this.store.log('warn', 'learning', 'learning_queue_overflow', { sceneId })
      return
    }
    const admitted = this.admission.admit(sceneId, message.id)
    if (admitted !== 'accepted') {
      admission.finish('skipped', {
        reason: admitted === 'duplicate' ? 'duplicate_source' : 'learning_queue_full',
        nextStep: 'none',
      })
      return
    }
    batch.push(structuredClone(message))
    this.learningInputTraces.set(`${sceneId}|${message.id}`, admission.context.traceId)
    this.learningBatches.set(sceneId, batch)
    this.learningCounts.accepted++
    admission.finish('completed', {
      counts: { queueDepth: batch.length },
      nextStep: 'learning_batch',
    })
    // This synchronous path just admitted this source; repair is for exceptional rescheduling.
    this.scheduleLearning(sceneId, false)
  }
  private oldestOverdueScene(maximumWait: number): string | undefined {
    const now = Date.now()
    let oldest: { sceneId: string; at: number } | undefined
    for (const sceneId of new Set([
      ...this.learningBatches.keys(),
      ...this.learningRetries.keys(),
    ])) {
      if (this.learningTasks.has(sceneId)) continue
      const retry = this.learningRetries.get(sceneId)
      const status = this.admission.inspect(sceneId)
      const at = retry
        ? retry.due + maximumWait
        : status.state === 'ready'
          ? status.firstAdmittedAt + maximumWait
          : Infinity
      if (
        at <= now &&
        (!oldest || at < oldest.at || (at === oldest.at && sceneId < oldest.sceneId))
      )
        oldest = { sceneId, at }
    }
    return oldest?.sceneId
  }
  private scheduleLearning(sceneId: string, repairAdmission = true) {
    if (
      this.disposed ||
      (!this.learningBatches.get(sceneId)?.length && !this.learningRetries.has(sceneId)) ||
      this.learningTasks.has(sceneId)
    )
      return
    const retry = this.learningRetries.get(sceneId)
    // Payloads are the existing bounded, authorized pending queue. Repair metadata
    // only; never restore sources from completed/failed/unknown batches.
    for (const message of repairAdmission ? (this.learningBatches.get(sceneId) ?? []) : []) {
      if (message.status === 'available' && !this.store.recalled(sceneId, message.id))
        this.admission.admit(sceneId, message.id)
    }
    const status = this.admission.inspect(sceneId)
    if (!retry && ['idle', 'disabled', 'active'].includes(status.state)) return
    const delay = retry
      ? Math.max(0, retry.due - Date.now())
      : status.state === 'blocked'
        ? 1000
        : 'dueAt' in status
          ? Math.max(0, status.dueAt - Date.now())
          : 1000
    const timerDelay = Math.max(this.ports.foregroundBusy() ? 1000 : 0, delay)
    const dueAt = Date.now() + timerDelay
    // A stream of new arrivals must not keep postponing the busy-work retry.
    // An early wake still rechecks current admission windows before taking a batch.
    if (
      this.learningTimers.has(sceneId) &&
      (this.learningTimerDue.get(sceneId) ?? Infinity) <= dueAt
    )
      return
    clearTimeout(this.learningTimers.get(sceneId))
    const timer = setTimeout(() => {
      this.learningTimers.delete(sceneId)
      this.learningTimerDue.delete(sceneId)
      const currentRetry = this.learningRetries.get(sceneId)
      const current = this.admission.inspect(sceneId)
      const settings = this.store.settings
      const maximumWait = Math.max(settings.learningBatchMs, settings.learningMaxWaitMs)
      const overdueAt = currentRetry
        ? currentRetry.due + maximumWait
        : 'firstAdmittedAt' in current
          ? current.firstAdmittedAt + maximumWait
          : Infinity
      const foregroundBusy = this.ports.foregroundBusy()
      const overdue = foregroundBusy && Date.now() >= overdueAt
      if (
        foregroundBusy &&
        (!overdue ||
          this.overdueLearningScene !== undefined ||
          this.oldestOverdueScene(maximumWait) !== sceneId)
      ) {
        this.scheduleLearning(sceneId, false)
        return
      }
      if (currentRetry) {
        void withModelPriority(1, () => this.learn(sceneId, undefined, overdue, overdueAt))
        return
      }
      const ticket = this.admission.take(sceneId, 60)
      if (ticket) void withModelPriority(1, () => this.learn(sceneId, ticket, overdue, overdueAt))
      else this.scheduleLearning(sceneId, false)
    }, timerDelay)
    timer.unref()
    this.learningTimers.set(sceneId, timer)
    this.learningTimerDue.set(sceneId, dueAt)
  }
  private async learn(
    sceneId: string,
    ticket?: LearningBatch,
    overdue = false,
    fairAfter = Date.now() +
      Math.max(this.store.settings.learningBatchMs, this.store.settings.learningMaxWaitMs),
  ) {
    if (this.disposed || !this.store.scene(sceneId)?.learningEnabled) {
      this.cancelLearning(sceneId)
      return
    }
    if (this.learningTasks.has(sceneId)) return
    const retry = this.learningRetries.get(sceneId)
    if (retry && retry.due > Date.now()) {
      this.scheduleLearning(sceneId)
      return
    }
    this.learningRetries.delete(sceneId)
    const queue = this.learningBatches.get(sceneId) ?? []
    // Admission order and payload order may differ after a pre-commit recall.
    // Consume precisely the authorized ticket, never a positional batch.
    const selectedIds = ticket ? new Set(ticket.sourceKeys) : undefined
    const selected =
      retry?.messages ??
      (selectedIds ? queue.filter((message) => selectedIds.has(message.id)) : queue.splice(0, 60))
    if (selectedIds && !retry)
      for (let index = queue.length - 1; index >= 0; index--) {
        if (selectedIds.has(queue[index]!.id)) queue.splice(index, 1)
      }
    const messages = [...new Map(selected.map((message) => [message.id, message])).values()].filter(
      (message) => message.status === 'available' && !this.store.recalled(sceneId, message.id),
    )
    if (selectedIds && selectedIds.size > messages.length)
      this.store.log('warn', 'learning', 'learning_source_payload_unavailable', {
        sceneId,
        counts: { inputCount: selectedIds.size, acceptedCount: messages.length },
      })
    if (!queue.length) this.learningBatches.delete(sceneId)
    if (!messages.length) {
      if (ticket) this.admission.finish(sceneId, ticket.token)
      this.scheduleLearning(sceneId)
      return
    }
    const attempt = retry?.attempt ?? 1
    const controller = new AbortController()
    this.learningTasks.set(sceneId, controller)
    this.learningActiveMessages.set(sceneId, messages)
    this.learningSurvivors.set(sceneId, messages)
    if (overdue) this.overdueLearningScene = sceneId
    const batchId = retry?.batchId ?? randomUUID()
    const linkedTraceIds = retry?.linkedTraceIds ?? [
      ...new Set(
        messages
          .map((message) => this.learningInputTraces.get(`${sceneId}|${message.id}`))
          .filter((id): id is string => !!id),
      ),
    ]
    const parentTraceId =
      retry?.parentTraceId ??
      messages
        .map((message) => this.learningInputTraces.get(`${sceneId}|${message.id}`))
        .find(Boolean)
    for (const message of messages) this.learningInputTraces.delete(`${sceneId}|${message.id}`)
    const run = () =>
      withBudgetContext({ turnId: batchId, lane: 'realtime' }, () =>
        withFlowContext(
          {
            traceId: batchId,
            batchId,
            sceneId,
            parentTraceId,
            linkedTraceIds,
            turnId: undefined,
            spanId: undefined,
          },
          async () => {
            const batchSpan = startFlow(this.store, 'learning.batch', {
              counts: { inputCount: messages.length, attempt },
            })
            batchSpan.bindSignal(controller.signal)
            await batchSpan.run(async () => {
              let terminal = false
              const progress: CommitProgress = {
                applying: false,
                writePending: false,
                totals: { candidateCount: 0, savedCount: 0, rejectedCount: 0, ignoredCount: 0 },
              }
              const { totals } = progress
              try {
                const completed = await runLearningBatch({
                  sceneId,
                  messages,
                  signal: controller.signal,
                  store: this.store,
                  ports: this.ports,
                  isDisposed: () => this.disposed,
                  progress,
                })
                if (!completed) return
                if (controller.signal.aborted) return
                this.learningCounts.completed += messages.length
                batchSpan.finish('completed', {
                  counts: totals,
                  reason: totals.candidateCount ? 'batch_processed' : 'no_learning_candidates',
                  nextStep: 'next_learning_batch',
                })
                terminal = true
              } catch (error) {
                if (
                  !controller.signal.aborted &&
                  !progress.applying &&
                  error instanceof ModelError &&
                  error.retryable &&
                  !providerAttemptsHandled(error) &&
                  attempt < this.store.settings.learningRetryAttempts
                ) {
                  const delay = Math.min(
                    30000,
                    this.store.settings.learningRetryBaseMs * 2 ** (attempt - 1),
                  )
                  this.learningRetries.set(sceneId, {
                    messages,
                    attempt: attempt + 1,
                    due: Date.now() + delay,
                    batchId,
                    parentTraceId,
                    linkedTraceIds,
                  })
                  batchSpan.finish(
                    'failed',
                    {
                      reason: modelFailureReason(error),
                      counts: { ...totals, retryDelayMs: delay },
                      nextStep: 'retry_scheduled',
                    },
                    error,
                  )
                  terminal = true
                  this.learningCounts.retried++
                  this.store.log('warn', 'learning', 'learning_retry_scheduled', {
                    sceneId,
                    durationMs: delay,
                  })
                } else {
                  if (!controller.signal.aborted) {
                    this.learningCounts.failed += messages.length
                    const id = randomUUID()
                    this.learningFailures.set(id, {
                      id,
                      sceneId,
                      attempt,
                      messageCount: messages.length,
                      partial: progress.applying,
                      canRetry: !progress.applying,
                      createdAt: new Date().toISOString(),
                      messages: progress.applying ? [] : messages,
                    })
                    // Metadata itself is bounded even for failures after partial writes.
                    while (
                      this.learningFailures.size > this.store.settings.learningQueueMaxTotalMessages
                    ) {
                      const oldest = this.learningFailures.keys().next().value!
                      const expired = this.learningFailures.get(oldest)!
                      this.learningFailures.delete(oldest)
                      this.store.log('warn', 'learning', 'learning_failure_evicted', {
                        sceneId: expired.sceneId,
                      })
                    }
                  }
                  const partialWrite =
                    totals.savedCount > 0 ||
                    progress.writePending ||
                    (!controller.signal.aborted && progress.applying)
                  batchSpan.finish(
                    partialWrite ? 'partial' : controller.signal.aborted ? 'cancelled' : 'failed',
                    {
                      reason: partialWrite
                        ? 'partial_commit_no_replay'
                        : controller.signal.aborted
                          ? 'request_cancelled'
                          : modelFailureReason(error),
                      counts: {
                        ...totals,
                        unconfirmedCandidateCount: progress.writePending ? 1 : 0,
                      },
                      nextStep: partialWrite
                        ? 'inspect_partial_learning'
                        : controller.signal.aborted
                          ? 'stop'
                          : 'manual_retry',
                    },
                    error,
                  )
                  terminal = true
                  this.store.log(
                    controller.signal.aborted ? 'info' : 'error',
                    'learning',
                    controller.signal.aborted
                      ? 'learning_cancelled'
                      : !progress.applying && error instanceof ModelError && error.retryable
                        ? 'learning_retry_exhausted'
                        : 'learning_failed',
                    {
                      sceneId,
                      ...currentFlowContext(),
                      ...sanitizeError(error, {
                        operation: 'learning.batch',
                        stage: 'learning.batch',
                        role: 'learning',
                      }),
                    },
                  )
                }
              } finally {
                if (!terminal)
                  batchSpan.finish(
                    totals.savedCount > 0 || progress.writePending ? 'partial' : 'cancelled',
                    {
                      reason:
                        totals.savedCount > 0 || progress.writePending
                          ? 'partial_commit_no_replay'
                          : 'request_cancelled',
                      counts: {
                        ...totals,
                        unconfirmedCandidateCount: progress.writePending ? 1 : 0,
                      },
                      nextStep:
                        totals.savedCount > 0 || progress.writePending
                          ? 'inspect_partial_learning'
                          : 'stop',
                    },
                  )
                // Only a pre-commit recall can safely reuse the unaffected input. Never replay
                // completed candidates or a response with uncertain partial storage side effects.
                if (
                  controller.signal.reason === 'source_recalled' &&
                  !progress.applying &&
                  !this.disposed &&
                  this.store.scene(sceneId)?.learningEnabled
                ) {
                  // recall() removes edited/recalled revisions from this live set. The
                  // original batch still belongs to the cancelled runner and must not replay.
                  const survivors = (this.learningSurvivors.get(sceneId) ?? []).filter(
                    (message) => !this.store.recalled(sceneId, message.id),
                  )
                  for (const message of survivors) this.admission.admit(sceneId, message.id)
                  this.learningBatches.set(sceneId, [
                    ...survivors,
                    ...(this.learningBatches.get(sceneId) ?? []),
                  ])
                }
                if (ticket) this.admission.finish(sceneId, ticket.token)
                this.learningActiveMessages.delete(sceneId)
                this.learningSurvivors.delete(sceneId)
                this.learningTasks.delete(sceneId)
                if (this.overdueLearningScene === sceneId) this.overdueLearningScene = undefined
                this.scheduleLearning(sceneId)
              }
            })
          },
          this.store,
        ),
      )
    await withOverdueLearning(run, fairAfter)
  }
}
