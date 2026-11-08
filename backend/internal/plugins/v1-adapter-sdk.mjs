// Generated from pinned TypeScript sources by generate-v1-adapters.mjs.
import { createHash } from 'node:crypto'
/** Bounded public reason codes only; arbitrary estimator/SQLite exception text is never forwarded. */
const budgetFailureCodes = [
  'budget_admission_failed',
  'budget_external_cost_unknown',
  'budget_restore_accounting_hold',
  'budget_amount_overflow',
  'budget_estimate_unknown',
  'budget_price_unknown',
  'budget_contract_overrun',
  'budget_daily_limit',
  'budget_lane_limit',
  'budget_turn_amount',
  'budget_turn_calls',
  'budget_turn_tokens',
  'budget_turn_identity_required',
  'budget_unbounded_exposure',
  'budget_unresolved_limit',
  'budget_output_cap_required',
  'budget_output_cap_unverified',
  'budget_protocol_unsupported',
  'budget_opaque_input_unsupported',
  'budget_verified_estimator_required',
  'budget_estimate_contract_mismatch',
  'budget_attempt_already_started',
  'budget_attempt_identity_conflict',
  'budget_idempotency_conflict',
  'budget_invalid_transition',
  'budget_attempt_owner',
  'budget_attempt_missing',
  'budget_release_started',
  'budget_receipt_conflict',
  'budget_configuration_invalid',
  'budget_configuration_changed',
]
function isBudgetFailureCode(value) {
  return budgetFailureCodes.includes(value)
}
function safeBudgetFailureCode(value) {
  const code = value instanceof Error ? value.message : value
  if (typeof code === 'string' && isBudgetFailureCode(code)) return code
  if (
    typeof code === 'string' &&
    [
      'invalid_budget_category',
      'invalid_budget_integer',
      'invalid_budget_price',
      'invalid_budget_estimate',
      'invalid_budget_identity',
      'invalid_budget_metadata',
      'invalid_budget_mode',
      'invalid_budget_request',
    ].includes(code)
  )
    return 'budget_configuration_invalid'
  return 'budget_admission_failed'
}

const contexts = new WeakMap()
const outcomes = new WeakMap()
function safeErrorContext(value) {
  const result = {}
  for (const key of ['operation', 'stage', 'role', 'modelId', 'providerId', 'apiModel']) {
    const field = value[key]
    if (
      typeof field === 'string' &&
      /^[A-Za-z0-9_.:@/ -]{1,128}$/.test(field) &&
      !field.includes('://')
    )
      result[key] = field
  }
  for (const key of [
    'attempt',
    'maxAttempts',
    'stageBudgetMs',
    'timeoutMs',
    'remainingCandidates',
  ]) {
    const field = value[key]
    if (typeof field === 'number' && Number.isFinite(field) && field >= 0) result[key] = field
  }
  if (typeof value.retryable === 'boolean') result.retryable = value.retryable
  return result
}
/** Inner, more specific metadata takes precedence over outer wrappers. */
function preserveErrorContext(error, value) {
  if (error && typeof error === 'object')
    contexts.set(error, { ...safeErrorContext(value), ...contexts.get(error) })
  return error
}
function errorContext(error) {
  return contexts.get(error)
}
function preserveOutcomeError(outcome, error, value = {}) {
  outcomes.set(outcome, preserveErrorContext(error, value))
  return outcome
}
function outcomeError(outcome) {
  return outcomes.get(outcome)
}
function failureFromOutcomes(values) {
  const errors = values.map(outcomeError).filter((value) => value !== undefined)
  return errors.length
    ? new AggregateError(errors, 'operation_outcomes_failed', { cause: errors.at(-1) })
    : undefined
}
/** Capture only the currently selected attempt when a shared stage deadline fires. */
function bindAbortErrorContext(signal, value) {
  if (!signal) return () => {}
  const apply = () => {
    const reason = signal.reason
    if (reason && typeof reason === 'object')
      contexts.set(reason, { ...contexts.get(reason), ...safeErrorContext(value) })
  }
  if (signal.aborted) apply()
  else signal.addEventListener('abort', apply, { once: true })
  return () => signal.removeEventListener('abort', apply)
}

/** Only application-owned codes are eligible for diagnostic text and advice. */
const diagnosticCodes = new Set([
  ...budgetFailureCodes,
  'memory_read_disabled',
  'identity_query_limited',
  'knowledge_anchor_not_exposed',
  'knowledge_anchor_unavailable',
  'knowledge_query_invalid',
  'knowledge_dependency_limit',
  'knowledge_policy_invalid',
  'knowledge_exposure_page_invalid',
  'knowledge_exposure_scope_invalid',
  'knowledge_read_dependency_stale',
  'knowledge_read_dependency_unavailable',
  'memory_read_revoked',
  'personalization_disabled',
  'memory_proof_budget_exhausted',
  'context_reference_unknown',
  'context_reference_cross_scene',
  'http_action_failed',
  'http_request_failed',
  'invalid_http_response',
  'platform_action_failed',
  'invalid_action_parameters',
  'missing_send_receipt',
  'send_failed',
  'outbound_encoding_busy',
  'outbound_memory_unavailable',
  'outbound_encoding_failed',
  'outbound_media_invalid',
  'outbound_media_too_large',
  'outbound_wire_too_large',
  'planner_timeout',
  'dialogue_timeout',
  'dialogue_failed',
  'dialogue_forbidden_tool',
  'dialogue_timeout',
  'planner_action',
  'planner_action_failed',
  'planner_ambiguous_plan',
  'planner_context_ineligible',
  'planner_duplicate_action',
  'planner_duplicate_evidence',
  'planner_evidence_too_large',
  'planner_evidence_budget_exhausted',
  'reply_evidence_budget_exhausted',
  'prompt_context_message_budget_exhausted',
  'prompt_context_target_budget_exhausted',
  'planner_failed',
  'planner_forbidden_action',
  'planner_forbidden_tool',
  'planner_invalid_delivery_tool',
  'planner_invalid_no_reply',
  'planner_invalid_plan',
  'planner_invalid_target',
  'planner_invalid_topic',
  'planner_missing_decision',
  'planner_missing_plan',
  'planner_no_reply',
  'planner_reason_omitted_over_limit',
  'planner_round_budget_exhausted',
  'planner_stale_action',
  'planner_timeout',
  'planner_tool_budget_exhausted',
  'planner_tool_failed',
  'planner_unknown_asset',
  'planner_unknown_evidence',
  'planner_unknown_memory_evidence',
  'planner_incomplete_relation_evidence',
  'planner_unknown_state_evidence',
  'planner_unknown_target',
  'planner_target_outside_admitted_topic',
  'image_processing_failed',
  'turn_tool_failures',
  'database_statement_failed',
  'database_io_error',
  'database_open_failed',
  'database_permission_denied',
  'database_schema_changed',
  'unknown_error',
  'cause_not_captured',
  'operation_outcomes_failed',
  'tool_execution_failed',
  'schema_validation_failed',
  'request_cancelled',
  'request_timeout',
  'missing_send_receipt',
  'adapter_not_connected',
  'platform_action_failed',
  'invalid_action_parameters',
  'http_request_failed',
  'http_action_failed',
  'protocol_disabled',
  'route_not_configured',
  'invalid_tool_names',
  'all_models_failed',
  'no_compatible_model',
  'embedding_model_not_configured',
  'invalid_embedding_dimensions_setting',
  'invalid_embedding_batch_size',
  'invalid_embedding_response',
  'invalid_embedding_dimensions',
  'invalid_embedding_vector',
  'embedding_dimensions_mismatch',
  'provider_not_configured',
  'provider_timeout',
  'invalid_provider_request_policy',
  'protocol_configuration_error',
  'model_stage_timeout',
  'invalid_role_budget',
  'provider_request_failed',
  'invalid_provider_url',
  'credential_not_configured',
  'invalid_provider_response',
  'invalid_provider_content_type',
  'empty_provider_response',
  'provider_response_too_large',
  'invalid_provider_json',
  'protocol_not_registered',
  'protocol_operation_unsupported',
  'work_queue_full',
  'invalid_provider_usage',
  'duplicate_tool_call_id',
  'incomplete_model_response',
  'invalid_provider_stream',
  'provider_stream_error',
  'provider_stream_invalid',
  'provider_stream_interrupted',
  'provider_stream_invalid_json',
  'provider_stream_protocol_mismatch',
  'provider_stream_event_mismatch',
  'provider_stream_upstream_error',
  'provider_stream_transport_error',
  'provider_stream_invalid_utf8',
  'model_response_refused',
  'incomplete_tool_call',
  'unsupported_response_item',
  'invalid_tool_arguments',
  'unknown_tool_call',
  'missing_tool_call_id',
  'unsupported_tool_call',
  'image_read_unsupported',
  'media_size_limit',
  'invalid_image_data',
  'invalid_media_reference',
  'media_scope_mismatch',
  'unsupported_media_type',
  'media_cancelled',
  'image_download_url_unavailable',
  'unsupported_image_format',
  'invalid_or_unsupported_image',
  'media_fetch_failed',
  'media_host_not_allowed',
  'media_address_not_allowed',
  'media_redirect_not_allowed',
  'image_unavailable',
  'adapter_unavailable',
  'delivery_failed',
  'delivery_unknown',
  'send_invalidated',
  'send_validation_failed',
  'database_busy',
  'database_locked',
  'database_readonly',
  'database_full',
  'database_corrupt',
  'database_constraint',
])
function knownDiagnosticCode(value) {
  return typeof value === 'string' &&
    (diagnosticCodes.has(value) || /^provider_http_[1-5]\d\d$/.test(value))
    ? value
    : undefined
}
const sqliteCodes = {
  SQLITE_ERROR: 'database_statement_failed',
  SQLITE_IOERR: 'database_io_error',
  SQLITE_CANTOPEN: 'database_open_failed',
  SQLITE_PERM: 'database_permission_denied',
  SQLITE_AUTH: 'database_permission_denied',
  SQLITE_SCHEMA: 'database_schema_changed',
  SQLITE_NOTADB: 'database_corrupt',
  SQLITE_BUSY: 'database_busy',
  SQLITE_LOCKED: 'database_locked',
  SQLITE_READONLY: 'database_readonly',
  SQLITE_FULL: 'database_full',
  SQLITE_CORRUPT: 'database_corrupt',
  SQLITE_CONSTRAINT: 'database_constraint',
}
/** Preserve technical information without copying arbitrary provider/chat payloads. */
function technicalMessage(message, code, type) {
  if (code !== 'unknown_error' && code !== 'cause_not_captured') return code
  if (typeof message !== 'string') return 'No inspectable error message was captured.'
  if (
    /^Cannot read properties of (?:undefined|null) \(reading ['"][^\n]{1,120}['"]\)$/.test(message)
  )
    return 'Cannot read a property of undefined or null (property name withheld).'
  if (/^Cannot set properties of (?:undefined|null)/.test(message))
    return 'Cannot set a property of undefined or null.'
  if (type === 'SyntaxError') return 'Invalid syntax or JSON; input content omitted.'
  if (
    /^(?:fetch failed|network error|socket hang up|The operation was aborted|This operation was aborted)\.?$/i.test(
      message,
    )
  )
    return message.slice(0, 120)
  if (/^database (?:is locked|is readonly|disk image is malformed)$/i.test(message)) return message
  if (
    /^(?:connect|read|write|getaddrinfo) (?:ECONNREFUSED|ECONNRESET|ETIMEDOUT|ENOTFOUND|EAI_AGAIN)\b/.test(
      message,
    )
  )
    return message.split(/\s+/).slice(0, 2).join(' ') + ' [endpoint omitted]'
  return 'Unclassified exception; arbitrary message content withheld. Inspect the error type and source frames.'
}

function awaitSendStage(action, signal) {
  return new Promise((resolve, reject) => {
    let invoking = true,
      abortQueued = false,
      settled = false
    const cleanup = () => signal.removeEventListener('abort', abort)
    const abort = () => {
      if (invoking || abortQueued || settled) return
      abortQueued = true
      queueMicrotask(() => {
        if (settled) return
        settled = true
        cleanup()
        reject(signal.reason ?? new Error('request_cancelled'))
      })
    }
    if (signal.aborted) return reject(signal.reason ?? new Error('request_cancelled'))
    signal.addEventListener('abort', abort, { once: true })
    let pending
    try {
      pending = Promise.resolve(action())
    } catch (error) {
      settled = true
      cleanup()
      reject(error)
      return
    } finally {
      invoking = false
    }
    // Observe a receipt already settled by the invocation before its synchronous abort.
    // A still-pending promise cannot win by settling after an awaited disable/cleanup.
    pending.then(
      (value) => {
        if (!settled) {
          settled = true
          cleanup()
          resolve(value)
        }
      },
      (error) => {
        if (!settled) {
          settled = true
          cleanup()
          reject(error)
        }
      },
    )
    if (signal.aborted) abort()
  })
}

const abortContainedAdapter = (adapter) => adapter
const hash = (value) => createHash('sha256').update(value).digest('hex')
function untilAbort(promise, signal) {
  if (signal.aborted) {
    void promise.catch(() => {})
    return Promise.reject(new Error('plugin_disabled'))
  }
  return new Promise((resolve, reject) => {
    const abort = () => reject(new Error('plugin_disabled'))
    signal.addEventListener('abort', abort, { once: true })
    promise.then(resolve, reject).finally(() => signal.removeEventListener('abort', abort))
  })
}
/** Trusted transport code receives scoped ingress and media access, never a direct core-send bypass. */
function installAdapter(id, factory, runtime, store, signal, services) {
  let installed = false,
    disposed = false
  let status = 'disconnected'
  const scene = (accountId, kind, platformSceneId) => {
    if (!accountId || !platformSceneId || !['group', 'private'].includes(kind))
      throw new Error('invalid_adapter_scene')
    return {
      id: `${id}:${hash(JSON.stringify([accountId, kind, platformSceneId]))}`,
      adapterId: id,
      accountId,
      kind,
      platformSceneId,
    }
  }
  const checkScene = (value) => {
    if (
      value.adapterId !== id ||
      value.id !== scene(value.accountId, value.kind, value.platformSceneId).id
    )
      throw new Error('adapter_scene_mismatch')
  }
  const checkMessage = (message, scope) => {
    checkScene(scope)
    if (
      message.sceneId !== scope.id ||
      !message.id.startsWith(scope.id + ':') ||
      (message.replyToId !== undefined && !message.replyToId.startsWith(scope.id + ':')) ||
      !message.senderId ||
      !['live', 'history'].includes(message.origin) ||
      !['available', 'recalled', 'unavailable'].includes(message.status) ||
      !Number.isFinite(Date.parse(message.sentAt)) ||
      !Number.isFinite(Date.parse(message.receivedAt)) ||
      !Array.isArray(message.parts) ||
      message.parts.length > 64 ||
      message.parts.some(
        (part) =>
          !part ||
          (part.type === 'text'
            ? typeof part.text !== 'string' || part.text.length > 12000
            : part.type === 'mention'
              ? typeof part.userId !== 'string'
              : part.type === 'media'
                ? typeof part.assetId !== 'string' || typeof part.mediaType !== 'string'
                : part.type === 'unsupported'
                  ? typeof part.description !== 'string'
                  : true),
      )
    )
      throw new Error('invalid_adapter_message')
    const value = {
      id: message.id,
      sceneId: message.sceneId,
      senderId: message.senderId,
      sentAt: message.sentAt,
      receivedAt: message.receivedAt,
      origin: message.origin,
      status: message.status,
      ...(message.replyToId ? { replyToId: message.replyToId } : {}),
      parts: message.parts.map((part) =>
        part.type === 'text'
          ? { type: 'text', text: part.text }
          : part.type === 'mention'
            ? { type: 'mention', userId: part.userId }
            : part.type === 'media'
              ? {
                  type: 'media',
                  assetId: part.assetId,
                  mediaType: part.mediaType,
                }
              : { type: 'unsupported', description: part.description },
      ),
    }
    if (store.recalled(scope.id, message.id) || value.status !== 'available') {
      value.parts = []
      value.status = store.recalled(scope.id, message.id) ? 'recalled' : value.status
    }
    return value
  }
  const active = () => installed && !disposed && !signal.aborted
  const adapter = factory({
    id,
    signal,
    scene,
    messageId: (scope, raw) => {
      checkScene(scope)
      return `${scope.id}:${hash(raw)}`
    },
    receive: (message, scope, isBot) => {
      if (!active()) return
      if (typeof isBot !== 'boolean') throw new Error('adapter_bot_flag_required')
      const normalized = checkMessage(message, scope)
      if (normalized.origin !== 'live') throw new Error('adapter_ingress_requires_live_origin')
      runtime.ingest(
        isBot ? { ...normalized, senderId: 'bot' } : normalized,
        Object.freeze({ ...scope }),
      )
    },
    edit: (message, scope, change) => {
      if (!active()) throw new Error('plugin_disabled')
      const normalized = checkMessage(message, scope)
      if (normalized.origin !== 'live') throw new Error('adapter_ingress_requires_live_origin')
      return runtime.edit(normalized, Object.freeze({ ...scope }), change)
    },
    recall: (scope, messageId) => {
      if (!active()) return
      checkScene(scope)
      if (!messageId.startsWith(scope.id + ':')) throw new Error('adapter_message_scope_mismatch')
      runtime.recall(scope.id, messageId)
    },
    isRecalled: (scope, messageId) => {
      if (!active()) throw new Error('plugin_disabled')
      checkScene(scope)
      return store.recalled(scope.id, messageId)
    },
    status: (value) => {
      if (
        !signal.aborted &&
        !disposed &&
        ['connecting', 'connected', 'disconnected'].includes(value)
      )
        status = value
    },
    media: (assetId, scope) => {
      checkScene(scope)
      if (!active()) throw new Error('plugin_disabled')
      return services.media?.(assetId, scope.id)
    },
  })
  const linked = (other) => (other ? AbortSignal.any([signal, other]) : signal)
  const read = async (scope, action, other) => {
    checkScene(scope)
    const combined = linked(other)
    if (!active() || combined.aborted) throw new Error('plugin_disabled')
    const value = await untilAbort(action(combined), combined)
    if (!active() || combined.aborted) throw new Error('plugin_disabled')
    if (
      !['complete', 'partial', 'unavailable'].includes(value.coverage) ||
      !Array.isArray(value.items) ||
      value.items.length > 1000
    )
      throw new Error('invalid_adapter_history')
    return {
      ...value,
      items: value.items
        .map((message) => ({
          ...checkMessage(message, scope),
          origin: 'history',
        }))
        .filter((message) => message.status !== 'recalled'),
    }
  }
  runtime.addAdapter(
    id,
    abortContainedAdapter({
      get status() {
        return status
      },
      send: async (scope, segment, other, beforeTransmit) => {
        checkScene(scope)
        if (segment.replyToId && !segment.replyToId.startsWith(scope.id + ':'))
          return { status: 'failed', detail: 'quote_scope_mismatch' }
        const combined = linked(other)
        if (!active() || combined.aborted)
          return { status: 'cancelled', detail: 'cancelled_before_send' }
        try {
          const result = await awaitSendStage(
            () =>
              adapter.send(
                Object.freeze({ ...scope }),
                structuredClone(segment),
                combined,
                beforeTransmit,
              ),
            combined,
          )
          if (
            !['sent', 'failed', 'unknown', 'cancelled'].includes(result.status) ||
            (result.messageId && !result.messageId.startsWith(scope.id + ':'))
          )
            return { status: 'unknown', detail: 'invalid_send_receipt' }
          const receipt = {
            status: result.status,
            ...(result.messageId ? { messageId: result.messageId } : {}),
            ...(knownDiagnosticCode(result.detail) ? { detail: result.detail } : {}),
          }
          const error = outcomeError(result)
          return error === undefined ? receipt : preserveOutcomeError(receipt, error)
        } catch (error) {
          return preserveOutcomeError(
            {
              status: 'unknown',
              detail: combined.aborted
                ? combined.reason?.name === 'TimeoutError'
                  ? 'request_timeout'
                  : 'request_cancelled'
                : 'delivery_unknown',
            },
            combined.aborted ? (combined.reason ?? error) : error,
            { operation: 'adapter.send', stage: 'adapter_send' },
          )
        }
      },
      getMessage: (scope, messageId, other) => {
        if (!messageId.startsWith(scope.id + ':'))
          return Promise.reject(new Error('adapter_message_scope_mismatch'))
        return read(
          scope,
          (signal) => adapter.getMessage(Object.freeze({ ...scope }), messageId, signal),
          other,
        ).then((result) => ({
          ...result,
          items: result.items.filter((message) => message.id === messageId),
        }))
      },
      history: (scope, input, other) =>
        read(
          scope,
          (signal) => adapter.history(Object.freeze({ ...scope }), structuredClone(input), signal),
          other,
        ),
      dispose: () => {
        if (disposed) return
        disposed = true
        installed = false
        status = 'disconnected'
        adapter.dispose()
      },
    }),
  )
  installed = true
  return {
    start: () =>
      untilAbort(
        Promise.resolve().then(() => {
          if (!active()) throw new Error('plugin_disabled')
          return adapter.start?.()
        }),
        signal,
      ),
    dispose: () => {
      try {
        runtime.removeAdapter(id)
      } finally {
        services.adapterStopped?.(id)
      }
    },
  }
}
