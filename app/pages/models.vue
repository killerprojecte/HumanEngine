<script setup lang="ts">
import { compactNumber, usageBars, usageLines, usageKeys } from '../utils/usage-chart'
import { formatUsageTime, formatUsageBucket } from '../utils/usage-time'
import { usageRange, type UsagePreset } from '../utils/usage-range'
import { createClientId } from '../utils/create-id'
import type { components, paths } from '~/types/api'
type Config = components['schemas']['ModelConfig']
type Provider = Config['providers'][number]
type Model = Config['models'][number]
type Route = Config['routes'][number]
type Capability = Model['capabilities'][number]
type ProbeResponse =
  paths['/api/models/{id}/probe']['post']['responses'][200]['content']['application/json']
type Section = 'providers' | 'models' | 'routes' | 'usage'
type EditorKind = 'provider' | 'model' | 'route'
const { t, locale } = useI18n()
const { client } = useApi()
const credentialInput = ref(''),
  credentialClear = ref(false)
const config = ref<Config>({ providers: [], models: [], routes: [] })
const loaded = ref(false),
  busy = ref(false),
  error = ref(''),
  editorError = ref(''),
  saved = ref(false)
const section = ref<Section>('models'),
  query = ref(''),
  providerFilter = ref('*')
const protocols = ref<Array<{ id: string; label: string }>>([])
const usage = ref<components['schemas']['Usage'][]>([]),
  usageBusy = ref(false)
const usagePreset = ref<UsagePreset>('24h')
const customStart = ref(''),
  customEnd = ref(''),
  usageError = ref('')
const usageIncomplete = ref(false),
  timelineIncomplete = ref(false)
const plannerUsageIncomplete = ref(false),
  plannerTimelineIncomplete = ref(false)
const usageTimezone = ref('')
const localTime = (value: string) => formatUsageTime(value, locale.value, usageTimezone.value)
const localBucket = (value: string, compact = false) =>
  formatUsageBucket(value, locale.value, usageTimezone.value, compact)
const selectedRange = ref<{ start: string; end: string }>()
const timeline = ref<components['schemas']['Usage'][]>([])
const rangeOptions = computed(() =>
  ['24h', 'today', 'yesterday', '7d', '14d', 'month', 'custom'].map((value) => ({
    value,
    label: t(`usageRange_${value}`),
  })),
)
type CacheSummary = { input: number; read: number; calls: number; known: number; readKnown: number }
const emptyCacheSummary = (): CacheSummary => ({
  input: 0,
  read: 0,
  calls: 0,
  known: 0,
  readKnown: 0,
})
const addCacheUsage = (total: CacheSummary, row: components['schemas']['Usage']) => {
  total.input += row.inputTokens
  total.read += row.cacheReadInputTokens
  total.calls += row.calls
  total.known += row.unknownUsageCalls
  total.readKnown += row.cacheReadKnownCalls
  return total
}
const plannerTotals = computed(() =>
  usage.value.filter((row) => row.role === 'planner').reduce(addCacheUsage, emptyCacheSummary()),
)
const plannerGroups = computed(() => {
  const groups = new Map<
    string,
    CacheSummary & { modelId: string; providerId: string; apiModelName: string }
  >()
  for (const row of usage.value) {
    if (row.role !== 'planner') continue
    const key = JSON.stringify([row.providerId, row.modelId, row.apiModelName])
    const group = groups.get(key) ?? {
      ...emptyCacheSummary(),
      modelId: row.modelId,
      providerId: row.providerId,
      apiModelName: row.apiModelName,
    }
    addCacheUsage(group, row)
    groups.set(key, group)
  }
  return [...groups.values()]
})
const timelineRows = computed(() => {
  const buckets = new Map<
    string,
    {
      at: string
      input: number
      output: number
      read: number
      write: number
      calls: number
      known: number
      readKnown: number
      writeKnown: number
      planner: CacheSummary
    }
  >()
  for (const row of timeline.value) {
    const point = buckets.get(row.day) ?? {
      at: row.day,
      input: 0,
      output: 0,
      read: 0,
      write: 0,
      calls: 0,
      known: 0,
      readKnown: 0,
      writeKnown: 0,
      planner: emptyCacheSummary(),
    }
    point.input += row.inputTokens
    point.output += row.outputTokens
    point.read += row.cacheReadInputTokens
    point.write += row.cacheWriteInputTokens
    point.calls += row.calls
    point.known += row.unknownUsageCalls
    point.readKnown += row.cacheReadKnownCalls
    point.writeKnown += row.cacheWriteKnownCalls
    if (row.role === 'planner') addCacheUsage(point.planner, row)
    buckets.set(row.day, point)
  }
  return [...buckets.values()].sort((a, b) => a.at.localeCompare(b.at))
})
const usageTotals = computed(() =>
  usage.value.reduce(
    (total, row) => ({
      calls: total.calls + row.calls,
      input: total.input + row.inputTokens,
      output: total.output + row.outputTokens,
      read: total.read + row.cacheReadInputTokens,
      known: total.known + row.unknownUsageCalls,
      readKnown: total.readKnown + row.cacheReadKnownCalls,
      usd: total.usd + row.estimatedUsd,
      priced: total.priced + row.pricedCalls,
      partial: total.partial + row.partialPricedCalls,
    }),
    {
      calls: 0,
      input: 0,
      output: 0,
      read: 0,
      known: 0,
      readKnown: 0,
      usd: 0,
      priced: 0,
      partial: 0,
    },
  ),
)
const chartMode = ref<'line' | 'bar'>('line')
const timelineMax = computed(() =>
  Math.max(1, ...timelineRows.value.flatMap((p) => usageBars(p).map((s) => s.value))),
)
const chartLines = computed(() =>
  usageKeys.map((key) => ({
    key,
    segments: usageLines(timelineRows.value, key, timelineMax.value),
  })),
)
const lineColors = { input: '#3b82f6', read: '#10b981', write: '#f59e0b', output: '#8b5cf6' }
const chartTicks = computed(() =>
  [1, 0.75, 0.5, 0.25, 0].map((ratio) => ({
    ratio,
    label: compactNumber(timelineMax.value * ratio),
  })),
)
const stackColors = {
  input: 'bg-blue-500',
  read: 'bg-emerald-500',
  write: 'bg-amber-400',
  output: 'bg-violet-500',
}
const stackKeys = usageKeys
const timelineTooltip = (point: (typeof timelineRows.value)[number]) => {
  const input = point.known ? t('usageCounterUnknown') : String(point.input)
  const output = point.known ? t('usageCounterUnknown') : String(point.output)
  const counter = (value: number, known: number) =>
    !known
      ? t('usageCounterUnknown')
      : known < point.calls
        ? t('usagePartialCounter', { value, known, total: point.calls })
        : String(value)
  return `${localBucket(point.at)} · ${t('usagePlannerCacheRate')}: ${plannerCacheRate(point.planner, plannerTimelineIncomplete.value)} · ${plannerCoverage(point.planner)} · ${t('usageInput')}: ${input} · ${t('usageOutput')}: ${output} · ${t('usageCacheRead')}: ${counter(point.read, point.readKnown)} · ${t('usageCacheWrite')}: ${counter(point.write, point.writeKnown)}`
}
const cacheRate = (point: {
  input: number
  read: number
  calls: number
  known: number
  readKnown: number
}) =>
  point.known === 0 && point.readKnown === point.calls && point.input > 0
    ? `${((point.read / point.input) * 100).toFixed(1)}%`
    : t('usageCounterUnknown')
// A partial historic interval must not be presented as a complete Planner rate.
const plannerCacheRate = (point: CacheSummary, incomplete = false) =>
  incomplete
    ? t('usagePlannerIncomplete')
    : point.calls === 0
      ? t('usagePlannerNoCalls')
      : cacheRate(point)
const plannerCoverage = (point: CacheSummary) =>
  t('usagePlannerCoverage', {
    known: compactNumber(point.readKnown),
    total: compactNumber(point.calls),
  })
const promptCacheOptions = computed(() =>
  ['auto', 'off', 'openai'].map((value) => ({ value, label: t(`mcPromptCache_${value}`) })),
)
const setPromptCacheMode = (value: unknown) => {
  if (!modelDraft.value || (value !== 'auto' && value !== 'off' && value !== 'openai')) return
  if (value === 'auto') delete modelDraft.value.promptCacheMode
  else modelDraft.value.promptCacheMode = value
}
const embeddingPanel = ref<{ refresh: () => Promise<void> }>()
const roles = ['planner', 'replyer', 'learning', 'vision', 'embedding'] as const
const priceFields = [
  { field: 'inputUsdPerMillion', label: 'inputPrice' },
  { field: 'outputUsdPerMillion', label: 'outputPrice' },
  { field: 'cacheReadUsdPerMillion', label: 'cacheReadPrice' },
  { field: 'cacheWriteUsdPerMillion', label: 'cacheWritePrice' },
  { field: 'cacheWrite5mUsdPerMillion', label: 'cacheWrite5mPrice' },
  { field: 'cacheWrite1hUsdPerMillion', label: 'cacheWrite1hPrice' },
  { field: 'cacheWrite30mUsdPerMillion', label: 'cacheWrite30mPrice' },
  { field: 'cacheWrite12hUsdPerMillion', label: 'cacheWrite12hPrice' },
] as const
type PriceField = (typeof priceFields)[number]['field']
const capabilities: Capability[] = ['text', 'tools', 'vision', 'embedding']
const capabilityKeys: Record<Capability, string> = {
  text: 'mcCapText',
  tools: 'mcCapTools',
  vision: 'mcCapVision',
  embedding: 'mcCapEmbedding',
}
const roleKeys: Record<Route['role'], string> = {
  planner: 'mcRolePlanner',
  thinker: 'mcRoleReplyer',
  replyer: 'mcRoleReplyer',
  learning: 'mcRoleLearning',
  vision: 'mcRoleVision',
  embedding: 'mcRoleEmbedding',
}
const capabilityLabel = (value: Capability) => t(capabilityKeys[value])
const roleLabel = (value: Route['role']) => t(roleKeys[value])
const roleHelp = (value: Route['role']) => t(`${roleKeys[value]}Help`)
const providerName = (id: string) => config.value.providers.find((p) => p.id === id)?.name ?? id
const modelName = (id: string) => config.value.models.find((m) => m.id === id)?.displayName ?? id
const modelChoice = (id: string) => {
  const model = config.value.models.find((m) => m.id === id)
  return model ? `${model.displayName} · ${providerName(model.providerId)}` : id
}
const getRoute = (role: Route['role']) =>
  config.value.routes.find((r) => r.role === role) ?? {
    role,
    orderedModelIds: [],
    timeoutMs: 180000,
  }
const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value))
const editorOpen = ref(false),
  editorKind = ref<EditorKind>('model'),
  isNew = ref(false)
const providerDraft = ref<Provider>(),
  modelDraft = ref<Model>(),
  routeDraft = ref<Route>()
const currentDraft = computed(() =>
  editorKind.value === 'provider'
    ? providerDraft.value
    : editorKind.value === 'model'
      ? modelDraft.value
      : routeDraft.value,
)
let openingSnapshot = ''
const dirty = computed(
  () =>
    editorOpen.value &&
    (JSON.stringify(currentDraft.value) !== openingSnapshot ||
      !!credentialInput.value ||
      credentialClear.value),
)
const confirmation = ref<'discard' | 'delete'>()
const confirmationKind = ref<'discard' | 'delete'>('discard')
watch(
  confirmation,
  (value) => {
    if (value) confirmationKind.value = value
  },
  { flush: 'sync' },
)
let pendingNavigation: ((allow: boolean) => void) | undefined
const discoveryBusy = ref(false),
  discoveryError = ref(''),
  discovered = ref<string[]>()
const probeBusy = ref(false),
  pendingProbe = ref(false),
  probeCapability = ref<Capability>('text')
const probeResults = ref<Record<string, ProbeResponse>>({})
const operationBusy = computed(() => busy.value || discoveryBusy.value || probeBusy.value)
const selectedProvider = computed(() => providerDraft.value)
const selectedModel = computed(() => modelDraft.value)
const selectedRoute = computed(() => routeDraft.value)
const inUseCount = computed(
  () => config.value.models.filter((m) => m.providerId === providerDraft.value?.id).length,
)
const affectedRoles = computed(
  () =>
    config.value.routes.filter((r) => r.orderedModelIds.includes(modelDraft.value?.id ?? ''))
      .length,
)
const editorTitle = computed(() =>
  t(
    editorKind.value === 'route'
      ? 'mcEditAssignment'
      : editorKind.value === 'provider'
        ? isNew.value
          ? 'mcAddProvider'
          : 'mcEditProvider'
        : isNew.value
          ? 'mcAddModel'
          : 'mcEditModel',
  ),
)
const editorDescription = computed(() =>
  editorKind.value === 'route' && routeDraft.value
    ? roleLabel(routeDraft.value.role)
    : editorKind.value === 'provider'
      ? providerDraft.value?.name
      : modelDraft.value?.displayName,
)
const saveLabel = computed(() =>
  t(
    editorKind.value === 'provider'
      ? 'mcSaveProvider'
      : editorKind.value === 'model'
        ? 'mcSaveModel'
        : 'mcSaveAssignment',
  ),
)
const protocolOptions = computed(() => [
  ...new Map(
    [
      ...protocols.value,
      ...config.value.providers
        .filter((p) => !protocols.value.some((x) => x.id === p.protocol))
        .map((p) => ({ id: p.protocol, label: `${p.protocol} (${t('unavailable')})` })),
    ].map((p) => [p.id, { label: p.label, value: p.id }]),
  ).values(),
])
const sections = computed(() => [
  { id: 'providers' as const, label: t('mcProviders') },
  { id: 'models' as const, label: t('mcModels') },
  { id: 'routes' as const, label: t('mcAssignments') },
  { id: 'usage' as const, label: t('mcUsage') },
])
const searchLabel = computed(() =>
  t(
    {
      providers: 'mcSearchProviders',
      models: 'mcSearchModels',
      routes: 'mcSearchRoles',
      usage: 'mcSearchUsage',
    }[section.value],
  ),
)
const matches = (...values: string[]) =>
  values.join(' ').toLocaleLowerCase().includes(query.value.toLocaleLowerCase().trim())
const filteredProviders = computed(() =>
  config.value.providers.filter((p) => matches(p.name, p.protocol, p.baseUrl)),
)
const filteredModels = computed(() =>
  config.value.models.filter(
    (m) =>
      (providerFilter.value === '*' || providerFilter.value === m.providerId) &&
      matches(
        m.displayName,
        m.apiModelName,
        providerName(m.providerId),
        ...m.capabilities.map(capabilityLabel),
      ),
  ),
)
const filteredRoles = computed(() =>
  roles.filter((role) =>
    matches(roleLabel(role), role, ...getRoute(role).orderedModelIds.map(modelName)),
  ),
)
const filteredUsage = computed(() =>
  usage.value.filter((row) =>
    matches(
      row.day,
      row.role,
      Object.hasOwn(roleKeys, row.role) ? roleLabel(row.role as Route['role']) : row.role,
      row.apiModelName,
      modelName(row.modelId),
    ),
  ),
)
async function load() {
  if (operationBusy.value) return
  busy.value = true
  error.value = ''
  saved.value = false
  try {
    const [models, available] = await Promise.all([
      client.GET('/api/models'),
      client.GET('/api/model-protocols'),
    ])
    if (!models.data) throw new Error(models.error?.error ?? t('networkError'))
    config.value = models.data
    probeResults.value = {}
    protocols.value = available.data ?? []
    loaded.value = true
    void embeddingPanel.value?.refresh()
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : t('networkError')
  } finally {
    busy.value = false
  }
}
let usageRequest = 0
async function loadUsage() {
  usageTimezone.value = Intl.DateTimeFormat().resolvedOptions().timeZone
  const range = usageRange(usagePreset.value, new Date(), customStart.value, customEnd.value)
  if (!range) {
    usageError.value = t('usageInvalidRange')
    return
  }
  const request = ++usageRequest
  usageBusy.value = true
  usageError.value = ''
  usageIncomplete.value = false
  timelineIncomplete.value = false
  plannerUsageIncomplete.value = false
  plannerTimelineIncomplete.value = false
  usage.value = []
  timeline.value = []
  selectedRange.value = range
  const bucket = Date.parse(range.end) - Date.parse(range.start) <= 2 * 86400000 ? 'hour' : 'day'
  try {
    const [result, points] = await Promise.all([
      client.GET('/api/usage', { params: { query: range } }),
      client.GET('/api/usage', {
        params: { query: { ...range, bucket, timeZone: usageTimezone.value } },
      }),
    ])
    if (request !== usageRequest) return
    if (!result.data || !points.data) {
      usageError.value = result.error?.error ?? points.error?.error ?? t('networkError')
      return
    }
    usage.value = result.data
    timeline.value = points.data
    usageIncomplete.value = result.response.headers.get('X-Usage-Incomplete') === 'true'
    timelineIncomplete.value = points.response.headers.get('X-Usage-Incomplete') === 'true'
    plannerUsageIncomplete.value =
      (result.response.headers.get('X-Usage-Planner-Incomplete') ??
        String(usageIncomplete.value)) === 'true'
    plannerTimelineIncomplete.value =
      (points.response.headers.get('X-Usage-Planner-Incomplete') ??
        String(timelineIncomplete.value)) === 'true'
  } catch {
    if (request === usageRequest) usageError.value = t('networkError')
  } finally {
    if (request === usageRequest) usageBusy.value = false
  }
}
watch(usagePreset, () => {
  if (usagePreset.value !== 'custom') void loadUsage()
})

onMounted(() => {
  usageTimezone.value = Intl.DateTimeFormat().resolvedOptions().timeZone
  void load()
  void loadUsage()
})
function addFirstProvider() {
  section.value = 'providers'
  beginEdit('provider')
}
function beginEdit(kind: EditorKind, id?: string) {
  if (operationBusy.value) return
  if (!loaded.value) {
    error.value = t('modelConfigNotReady')
    return
  }
  try {
    credentialInput.value = ''
    credentialClear.value = false
    editorKind.value = kind
    isNew.value = !id && kind !== 'route'
    providerDraft.value = undefined
    modelDraft.value = undefined
    routeDraft.value = undefined
    if (kind === 'provider')
      providerDraft.value = clone(
        id
          ? config.value.providers.find((p) => p.id === id)!
          : {
              id: createClientId(),
              name: '',
              protocol: 'openai-chat',
              baseUrl: 'https://api.openai.com/v1',
              credentialEnv: '',
              retryCount: 2,
              timeoutMs: 30000,
            },
      )
    if (kind === 'model') {
      if (!config.value.providers.length) {
        section.value = 'providers'
        return
      }
      modelDraft.value = clone(
        id
          ? config.value.models.find((m) => m.id === id)!
          : {
              id: createClientId(),
              displayName: '',
              providerId: config.value.providers[0]!.id,
              apiModelName: '',
              capabilities: ['text'],
              embeddingBatchSize: 4,
            },
      )
    }
    if (kind === 'route') routeDraft.value = clone(getRoute(id as Route['role']))
    openingSnapshot = JSON.stringify(currentDraft.value)
    editorError.value = ''
    saved.value = false
    discovered.value = undefined
    discoveryError.value = ''
    pendingProbe.value = false
    confirmation.value = undefined
    editorOpen.value = true
  } catch {
    editorOpen.value = false
    error.value = t('entityEditorUnavailable')
  }
}
function closeEditor() {
  credentialInput.value = ''
  credentialClear.value = false
  editorOpen.value = false
  confirmation.value = undefined
  pendingProbe.value = false
  editorError.value = ''
}
function requestClose(open = false) {
  if (open || operationBusy.value) return
  if (dirty.value) confirmation.value = 'discard'
  else closeEditor()
}
function cancelConfirmation() {
  confirmation.value = undefined
  pendingNavigation?.(false)
  pendingNavigation = undefined
}
function discardEditor() {
  closeEditor()
  pendingNavigation?.(true)
  pendingNavigation = undefined
}
onBeforeRouteLeave(() => {
  if (operationBusy.value) return false
  if (!editorOpen.value || !dirty.value) return true
  confirmation.value = 'discard'
  return new Promise<boolean>((resolve) => {
    pendingNavigation = resolve
  })
})
function warnBeforeUnload(event: BeforeUnloadEvent) {
  if (!editorOpen.value || !dirty.value) return
  event.preventDefault()
  event.returnValue = ''
}
onMounted(() => window.addEventListener('beforeunload', warnBeforeUnload))
onBeforeUnmount(() => {
  pendingNavigation?.(false)
  window.removeEventListener('beforeunload', warnBeforeUnload)
})
async function persist(deleting = false) {
  if (operationBusy.value || !currentDraft.value) return
  if (!deleting && providerDraft.value) {
    const { retryCount, timeoutMs } = providerDraft.value
    if (
      !Number.isInteger(retryCount) ||
      retryCount < 0 ||
      retryCount > 5 ||
      !Number.isInteger(timeoutMs) ||
      timeoutMs < 100 ||
      timeoutMs > 300000
    ) {
      editorError.value = t('mcProviderRequestInvalid')
      return
    }
  }
  if (!deleting && modelDraft.value && !modelDraft.value.capabilities.length) {
    editorError.value = t('mcRequiredCapability')
    return
  }
  if (
    !deleting &&
    modelDraft.value &&
    priceFields
      .map(({ field }) => modelDraft.value![field])
      .some((value) => value !== undefined && (!Number.isFinite(value) || value < 0))
  ) {
    editorError.value = t('priceInvalid')
    return
  }
  if (!deleting && modelDraft.value?.capabilities.includes('embedding')) {
    const dimensions = modelDraft.value.embeddingDimensions
    if (
      dimensions !== undefined &&
      (!Number.isInteger(dimensions) || dimensions < 1 || dimensions > 16384)
    ) {
      editorError.value = t('eiDimensionsInvalid')
      return
    }
    const size = modelDraft.value.embeddingBatchSize ?? 4
    if (!Number.isInteger(size) || size < 1 || size > 64) {
      editorError.value = t('mcEmbeddingBatchInvalid')
      return
    }
  }
  busy.value = true
  editorError.value = ''
  pendingProbe.value = false
  try {
    const response = await client.GET('/api/models')
    if (!response.data) throw new Error(response.error?.error ?? t('networkError'))
    const next = clone(response.data)
    if (providerDraft.value) {
      const draft = clone(providerDraft.value),
        index = next.providers.findIndex((p) => p.id === draft.id)
      if (!isNew.value && (index < 0 || JSON.stringify(next.providers[index]) !== openingSnapshot))
        throw new Error(t('mcConfigChanged'))
      if (deleting) {
        const count = next.models.filter((m) => m.providerId === draft.id).length
        if (count) throw new Error(t('mcProviderInUse', { count }))
        next.providers = next.providers.filter((p) => p.id !== draft.id)
      } else if (index < 0) next.providers.push(draft)
      else next.providers[index] = draft
    } else if (modelDraft.value) {
      const draft = clone(modelDraft.value),
        index = next.models.findIndex((m) => m.id === draft.id)
      if (!isNew.value && (index < 0 || JSON.stringify(next.models[index]) !== openingSnapshot))
        throw new Error(t('mcConfigChanged'))
      if (deleting) {
        next.models = next.models.filter((m) => m.id !== draft.id)
        for (const route of next.routes)
          route.orderedModelIds = route.orderedModelIds.filter((id) => id !== draft.id)
      } else if (index < 0) next.models.push(draft)
      else next.models[index] = draft
    } else if (routeDraft.value) {
      const draft = clone(routeDraft.value),
        index = next.routes.findIndex((r) => r.role === draft.role)
      const original = next.routes[index] ?? {
        role: draft.role,
        orderedModelIds: [],
        timeoutMs: 180000,
      }
      if (JSON.stringify(original) !== openingSnapshot) throw new Error(t('mcConfigChanged'))
      if (index < 0) next.routes.push(draft)
      else next.routes[index] = draft
    }
    const credentials =
      !deleting && providerDraft.value && (credentialInput.value || credentialClear.value)
        ? {
            [providerDraft.value.id]: credentialClear.value
              ? { action: 'clear' as const }
              : { action: 'set' as const, value: credentialInput.value },
          }
        : undefined
    const result = await client.PUT('/api/models', { body: { ...next, credentials } })
    if (!result.data) throw new Error(result.error?.error ?? t('networkError'))
    config.value = result.data
    probeResults.value = {}
    saved.value = true
    void embeddingPanel.value?.refresh()
    closeEditor()
  } catch (cause) {
    confirmation.value = undefined
    editorError.value = cause instanceof Error ? cause.message : t('networkError')
  } finally {
    busy.value = false
  }
}
const imageUploadFields = [
  { key: 'maxImageBytes', label: 'mcImageBytes', placeholder: '10485760' },
  { key: 'maxWidth', label: 'mcImageWidth', placeholder: '1536' },
  { key: 'maxHeight', label: 'mcImageHeight', placeholder: '1536' },
  { key: 'maxRequestBytes', label: 'mcImageRequestBytes', placeholder: '' },
] as const
function setImageUploadLimit(
  key: (typeof imageUploadFields)[number]['key'],
  value: string | number,
) {
  if (!selectedProvider.value) return
  const limits = { ...selectedProvider.value.imageUploadLimits }
  if (value === '') delete limits[key]
  else limits[key] = Number(value)
  selectedProvider.value.imageUploadLimits = Object.keys(limits).length ? limits : undefined
}
function setProviderField(
  field: 'name' | 'protocol' | 'baseUrl' | 'credentialEnv',
  value: string | number,
) {
  if (providerDraft.value) providerDraft.value[field] = String(value)
}
function price(field: PriceField, value: string | number) {
  if (!modelDraft.value) return
  if (value === '') delete modelDraft.value[field]
  else modelDraft.value[field] = Number(value)
}
function toggleCapability(capability: Capability, checked: boolean | 'indeterminate') {
  if (!modelDraft.value) return
  modelDraft.value.capabilities =
    checked === true
      ? [...new Set([...modelDraft.value.capabilities, capability])]
      : modelDraft.value.capabilities.filter((c) => c !== capability)
}
function move(index: number, delta: number) {
  const ids = routeDraft.value?.orderedModelIds,
    target = index + delta
  if (!ids || target < 0 || target >= ids.length) return
  ;[ids[index], ids[target]] = [ids[target]!, ids[index]!]
}
async function discover(id: string) {
  if (operationBusy.value || !config.value.providers.some((p) => p.id === id)) return
  discoveryBusy.value = true
  discoveryError.value = ''
  discovered.value = undefined
  try {
    const result = await client.GET('/api/providers/{id}/models', { params: { path: { id } } })
    if (result.data) discovered.value = result.data.models
    else discoveryError.value = t('mcDiscoveryFailure')
  } catch {
    discoveryError.value = t('mcDiscoveryFailure')
  } finally {
    discoveryBusy.value = false
  }
}
watch(
  () => modelDraft.value?.providerId,
  () => {
    discovered.value = undefined
    discoveryError.value = ''
  },
)
async function runProbe() {
  if (operationBusy.value || dirty.value || isNew.value || !modelDraft.value || !pendingProbe.value)
    return
  const id = modelDraft.value.id,
    capability = probeCapability.value
  probeBusy.value = true
  pendingProbe.value = false
  editorError.value = ''
  delete probeResults.value[id]
  try {
    const latest = await client.GET('/api/models')
    if (!latest.data) throw new Error(latest.error?.error ?? t('networkError'))
    const latestModel = latest.data.models.find((model) => model.id === id)
    const latestProvider = latest.data.providers.find(
      (provider) => provider.id === modelDraft.value?.providerId,
    )
    const shownProvider = config.value.providers.find(
      (provider) => provider.id === modelDraft.value?.providerId,
    )
    if (
      JSON.stringify(latestModel) !== openingSnapshot ||
      JSON.stringify(latestProvider) !== JSON.stringify(shownProvider)
    )
      throw new Error(t('mcConfigChanged'))
    const result = await client.POST('/api/models/{id}/probe', {
      params: { path: { id } },
      body: { capability },
    })
    if (result.data) probeResults.value[id] = result.data
    else editorError.value = result.error?.error ?? t('networkError')
    await loadUsage()
  } catch (cause) {
    editorError.value = cause instanceof Error ? cause.message : t('networkError')
  } finally {
    probeBusy.value = false
  }
}
function usd(value: number) {
  return new Intl.NumberFormat(locale.value, {
    style: 'currency',
    currency: 'USD',
    maximumFractionDigits: 6,
  }).format(value)
}
function cost(row: components['schemas']['Usage']) {
  return row.pricedCalls || row.partialPricedCalls ? usd(row.estimatedUsd) : t('usageUnknownCost')
}
watch(section, () => {
  query.value = ''
})
</script>
<template>
  <div class="model-settings space-y-5">
    <header class="flex items-start justify-between gap-4">
      <div>
        <h1 class="page-heading">{{ t('mcTitle') }}</h1>
        <p class="mt-2 text-sm text-muted">{{ t('mcIntro') }}</p>
      </div>
      <UButton
        variant="ghost"
        color="neutral"
        icon="i-lucide-refresh-cw"
        :loading="busy && !editorOpen"
        :disabled="operationBusy"
        :aria-label="t('mcRefresh')"
        @click="load"
      />
    </header>
    <UAlert v-if="error" color="error" :title="error" />
    <UAlert v-if="saved" color="success" variant="subtle" :title="t('mcSaved')" />
    <EmbeddingIndexPanel ref="embeddingPanel" :config-busy="operationBusy || editorOpen" />
    <section class="rounded-lg border border-default bg-default overflow-hidden">
      <nav
        class="flex overflow-x-auto border-b border-default px-4 gap-5"
        :aria-label="t('mcTitle')"
      >
        <button
          v-for="item in sections"
          :key="item.id"
          class="h-12 shrink-0 border-b-2 text-sm font-medium transition-colors"
          :class="
            section === item.id
              ? 'border-primary text-primary'
              : 'border-transparent text-muted hover:text-highlighted'
          "
          :aria-current="section === item.id ? 'page' : undefined"
          @click="section = item.id"
        >
          {{ item.label }}
        </button>
      </nav>
      <div class="flex flex-wrap items-center gap-3 p-4 border-b border-default">
        <UInput
          v-model="query"
          icon="i-lucide-search"
          :placeholder="searchLabel"
          :aria-label="searchLabel"
          class="w-full sm:w-72"
        />
        <USelect
          v-if="section === 'models'"
          v-model="providerFilter"
          :items="[
            { label: t('uxAllProviders'), value: '*' },
            ...config.providers.map((p) => ({ label: p.name, value: p.id })),
          ]"
          :aria-label="t('provider')"
          class="w-full sm:w-48"
        />
        <div class="hidden sm:block flex-1" />
        <UButton
          v-if="section === 'providers'"
          icon="i-lucide-plus"
          :disabled="operationBusy || !loaded"
          @click="beginEdit('provider')"
          >{{ t('mcAddProvider') }}</UButton
        >
        <UButton
          v-if="section === 'models' && config.providers.length"
          icon="i-lucide-plus"
          :disabled="operationBusy || !loaded"
          @click="beginEdit('model')"
          >{{ t('mcAddModel') }}</UButton
        >
        <UButton
          v-if="section === 'usage'"
          variant="soft"
          icon="i-lucide-refresh-cw"
          :loading="usageBusy"
          @click="loadUsage"
          >{{ t('refresh') }}</UButton
        >
      </div>
      <template v-if="section === 'models'">
        <div v-if="loaded && !config.providers.length" class="py-12 px-6 text-center space-y-3">
          <h2 class="font-semibold">{{ t('mcNoProviders') }}</h2>
          <p class="text-sm text-muted">{{ t('mcNoProvidersHelp') }}</p>
          <UButton :disabled="operationBusy || !loaded" @click="addFirstProvider">{{
            t('mcAddProvider')
          }}</UButton>
        </div>
        <div v-else-if="loaded && !config.models.length" class="py-12 px-6 text-center space-y-3">
          <h2 class="font-semibold">{{ t('mcNoModels') }}</h2>
          <p class="text-sm text-muted">{{ t('mcNoModelsHelp') }}</p>
          <UButton :disabled="operationBusy || !loaded" @click="beginEdit('model')">{{
            t('mcAddModel')
          }}</UButton>
        </div>
        <template v-else>
          <table class="model-table hidden md:table w-full table-fixed text-left text-sm">
            <colgroup>
              <col style="width: 42%" />
              <col style="width: 26%" />
              <col style="width: 20%" />
              <col style="width: 12%" />
            </colgroup>
            <thead>
              <tr>
                <th>{{ t('mcModelName') }}</th>
                <th>{{ t('provider') }}</th>
                <th>{{ t('mcTableCapabilities') }}</th>
                <th class="text-right">{{ t('mcActions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="model in filteredModels" :key="model.id">
                <td>
                  <div class="font-medium text-highlighted break-words">
                    {{ model.displayName }}
                  </div>
                  <div class="mt-1 font-mono text-xs leading-[18px] text-muted break-all">
                    {{ model.apiModelName || t('mcNoApiName') }}
                  </div>
                </td>
                <td>
                  <span class="break-words">{{ providerName(model.providerId) }}</span>
                </td>
                <td>
                  <div class="flex flex-wrap gap-x-2 gap-y-1 text-xs leading-5 text-muted">
                    <span v-for="cap in model.capabilities" :key="cap" class="whitespace-nowrap">{{
                      capabilityLabel(cap)
                    }}</span>
                  </div>
                </td>
                <td class="action-cell">
                  <UButton
                    variant="ghost"
                    color="neutral"
                    :disabled="operationBusy"
                    @click="beginEdit('model', model.id)"
                    >{{ t('edit') }}</UButton
                  >
                </td>
              </tr>
              <tr v-if="!filteredModels.length">
                <td colspan="4" class="text-center py-12 text-muted">{{ t('uxNoMatches') }}</td>
              </tr>
            </tbody>
          </table>
          <div class="md:hidden divide-y divide-default">
            <article v-for="model in filteredModels" :key="model.id" class="p-4 space-y-2">
              <div class="flex justify-between items-start gap-3">
                <h2 class="font-medium break-words min-w-0">{{ model.displayName }}</h2>
                <UButton
                  variant="ghost"
                  color="neutral"
                  class="shrink-0"
                  @click="beginEdit('model', model.id)"
                  >{{ t('edit') }}</UButton
                >
              </div>
              <p class="font-mono text-xs break-all text-muted">{{ model.apiModelName }}</p>
              <p class="text-sm break-words">
                {{ t('provider') }} · {{ providerName(model.providerId) }}
              </p>
              <p class="text-xs text-muted">
                {{ model.capabilities.map(capabilityLabel).join(' · ') }}
              </p>
            </article>
            <p v-if="!filteredModels.length" class="p-8 text-center text-muted">
              {{ t('uxNoMatches') }}
            </p>
          </div>
        </template>
      </template>
      <template v-else-if="section === 'providers'">
        <div v-if="loaded && !config.providers.length" class="py-12 px-6 text-center space-y-3">
          <h2 class="font-semibold">{{ t('mcNoProviders') }}</h2>
          <p class="text-sm text-muted">{{ t('mcNoProvidersHelp') }}</p>
          <UButton :disabled="operationBusy || !loaded" @click="beginEdit('provider')">{{
            t('mcAddProvider')
          }}</UButton>
        </div>
        <div v-else class="overflow-x-auto">
          <table class="model-table w-full table-fixed text-left text-sm min-w-[620px]">
            <colgroup>
              <col style="width: 27%" />
              <col style="width: 27%" />
              <col style="width: 34%" />
              <col style="width: 12%" />
            </colgroup>
            <thead>
              <tr>
                <th>{{ t('mcProviderName') }}</th>
                <th>{{ t('protocol') }}</th>
                <th>{{ t('baseUrl') }}</th>
                <th class="text-right">{{ t('mcActions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="provider in filteredProviders" :key="provider.id">
                <td>
                  <div class="font-medium break-words">{{ provider.name }}</div>
                  <p class="mt-1.5 text-xs text-muted">
                    {{ t('mcModels') }} ·
                    {{ config.models.filter((m) => m.providerId === provider.id).length }}
                  </p>
                </td>
                <td>
                  <p class="break-words">
                    {{
                      protocols.find((p) => p.id === provider.protocol)?.label ?? provider.protocol
                    }}
                  </p>
                  <p
                    v-if="!protocols.some((p) => p.id === provider.protocol)"
                    class="text-warning text-xs mt-1"
                  >
                    {{ t('unavailable') }}
                  </p>
                </td>
                <td class="font-mono text-xs break-all">{{ provider.baseUrl }}</td>
                <td class="action-cell">
                  <UButton
                    variant="ghost"
                    color="neutral"
                    :disabled="operationBusy"
                    @click="beginEdit('provider', provider.id)"
                    >{{ t('edit') }}</UButton
                  >
                </td>
              </tr>
              <tr v-if="!filteredProviders.length">
                <td colspan="4" class="text-center py-12 text-muted">{{ t('uxNoMatches') }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
      <template v-else-if="section === 'routes'">
        <p class="px-4 py-4 text-sm text-muted border-b border-default">
          {{ t('mcAssignmentHelp') }}
        </p>
        <article
          v-for="role in filteredRoles"
          :key="role"
          class="grid grid-cols-1 md:grid-cols-[220px_minmax(0,1fr)_76px] gap-4 p-4 border-b border-default last:border-0 items-start"
        >
          <div>
            <h2 class="font-medium text-sm">{{ roleLabel(role) }}</h2>
            <p class="text-xs leading-5 text-muted mt-1">{{ roleHelp(role) }}</p>
          </div>
          <ol class="space-y-2 text-sm">
            <li v-for="(id, index) in getRoute(role).orderedModelIds" :key="id" class="flex gap-3">
              <span class="w-16 shrink-0 text-xs text-muted pt-0.5">{{
                index ? t('mcFallback', { number: index }) : t('mcPrimary')
              }}</span
              ><span class="break-words min-w-0">{{ modelChoice(id) }}</span>
            </li>
            <li v-if="!getRoute(role).orderedModelIds.length" class="text-muted">
              {{ t('uxUnassigned') }}
            </li>
          </ol>
          <UButton
            color="neutral"
            variant="ghost"
            class="justify-self-end"
            :disabled="operationBusy"
            @click="beginEdit('route', role)"
            >{{ t('edit') }}</UButton
          >
        </article>
        <p v-if="!filteredRoles.length" class="p-8 text-center text-muted">
          {{ t('uxNoMatches') }}
        </p>
      </template>
      <template v-else>
        <div class="p-4 sm:p-6 space-y-6">
          <div class="flex flex-wrap items-end justify-between gap-4">
            <div>
              <h2 class="text-lg font-semibold tracking-tight">{{ t('usageOverview') }}</h2>
              <p class="text-sm text-muted mt-1">{{ t('usageOverviewHelp') }}</p>
            </div>
            <div class="flex flex-wrap items-end gap-3">
              <UFormField :label="t('usageTimeRange')"
                ><USelect v-model="usagePreset" :items="rangeOptions" class="min-w-40"
              /></UFormField>
              <template v-if="usagePreset === 'custom'">
                <UFormField :label="t('usageStart')"
                  ><UInput v-model="customStart" type="datetime-local"
                /></UFormField>
                <UFormField :label="t('usageEnd')"
                  ><UInput v-model="customEnd" type="datetime-local"
                /></UFormField>
                <UButton :loading="usageBusy" @click="loadUsage">{{ t('usageApply') }}</UButton>
              </template>
            </div>
          </div>
          <details class="text-xs text-muted rounded-lg border border-default px-3 py-2">
            <summary class="cursor-pointer">{{ t('usageAccountingDetails') }}</summary>
            <p class="mt-2">{{ t('usageTimezone', { timezone: usageTimezone }) }}</p>
            <p v-if="selectedRange" class="text-xs text-muted">
              {{ localTime(selectedRange.start) }} ≤ {{ t('usageTime') }} &lt;
              {{ localTime(selectedRange.end) }}
            </p>
            <p class="mt-2">{{ t('usageEstimateHelp') }}</p>
          </details>
          <p v-if="usageError" role="alert" class="text-error">{{ usageError }}</p>
          <p v-if="usageIncomplete || timelineIncomplete" role="status" class="text-warning">
            {{ t('usageIncompleteRange') }}
          </p>

          <div v-if="!usageBusy && !usageError" class="grid grid-cols-2 xl:grid-cols-4 gap-4">
            <div class="usage-stat">
              <div class="usage-stat-label">
                <UIcon name="i-lucide-wallet" />{{ t('usageCost') }}
              </div>
              <strong class="usage-stat-value">{{
                usageTotals.priced || usageTotals.partial
                  ? usd(usageTotals.usd)
                  : t('usageUnknownCost')
              }}</strong>
              <p class="usage-stat-note">
                {{
                  t('usagePriced', {
                    priced: compactNumber(usageTotals.priced),
                    total: compactNumber(usageTotals.calls),
                  })
                }}
              </p>
            </div>
            <div class="usage-stat">
              <div class="usage-stat-label">
                <UIcon name="i-lucide-layers" />{{ t('usageTotalTokens') }}
              </div>
              <strong
                class="usage-stat-value"
                :title="
                  usageTotals.known
                    ? t('usageCounterUnknown')
                    : String(usageTotals.input + usageTotals.output)
                "
                >{{
                  usageTotals.known
                    ? t('usageCounterUnknown')
                    : compactNumber(usageTotals.input + usageTotals.output)
                }}</strong
              >
              <p class="usage-stat-note">
                {{ t('usageInput') }}
                {{ usageTotals.known ? '—' : compactNumber(usageTotals.input) }} ·
                {{ t('usageOutput') }}
                {{ usageTotals.known ? '—' : compactNumber(usageTotals.output) }}
              </p>
            </div>
            <div class="usage-stat">
              <div class="usage-stat-label">
                <UIcon name="i-lucide-zap" />{{ t('usagePlannerCacheRate') }}
              </div>
              <strong class="usage-stat-value text-emerald-500">{{
                plannerCacheRate(plannerTotals, plannerUsageIncomplete)
              }}</strong>
              <p class="usage-stat-note">{{ t('usagePlannerWeightedRate') }}</p>
              <p class="usage-stat-note">{{ plannerCoverage(plannerTotals) }}</p>
            </div>
            <div class="usage-stat">
              <div class="usage-stat-label">
                <UIcon name="i-lucide-activity" />{{ t('usageCalls') }}
              </div>
              <strong class="usage-stat-value" :title="String(usageTotals.calls)">{{
                compactNumber(usageTotals.calls)
              }}</strong>
              <p class="usage-stat-note">{{ t('usageSelectedRange') }}</p>
            </div>
          </div>
          <details class="rounded-xl border border-default overflow-hidden">
            <summary class="px-4 py-3 text-sm font-medium cursor-pointer">
              {{ t('usagePlannerDetails') }}
            </summary>
            <p class="px-4 pb-3 text-xs text-muted">{{ t('usagePlannerScope') }}</p>
            <div v-if="plannerGroups.length" class="overflow-x-auto">
              <table class="model-table w-full text-left text-sm">
                <thead>
                  <tr>
                    <th>{{ t('provider') }} / {{ t('mcModelName') }}</th>
                    <th>{{ t('usageInput') }}</th>
                    <th>{{ t('usageCacheRead') }}</th>
                    <th>{{ t('usagePlannerCacheRate') }}</th>
                    <th>{{ t('usagePlannerCoverageLabel') }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr
                    v-for="group in plannerGroups"
                    :key="JSON.stringify([group.providerId, group.modelId, group.apiModelName])"
                  >
                    <td>
                      {{ providerName(group.providerId) }} · {{ modelName(group.modelId) }}
                      <p class="text-xs text-muted">{{ group.apiModelName }}</p>
                    </td>
                    <td :title="group.known ? t('usageCounterUnknown') : String(group.input)">
                      {{ group.known ? t('usageCounterUnknown') : compactNumber(group.input) }}
                    </td>
                    <td :title="group.readKnown ? String(group.read) : t('usageCounterUnknown')">
                      {{ group.readKnown ? compactNumber(group.read) : t('usageCounterUnknown') }}
                    </td>
                    <td>{{ plannerCacheRate(group, plannerUsageIncomplete) }}</td>
                    <td>{{ plannerCoverage(group) }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p v-else class="px-4 pb-4 text-sm text-muted">
              {{ plannerCacheRate(plannerTotals, plannerUsageIncomplete) }}
            </p>
          </details>
          <p class="text-xs text-muted">{{ t('usageWholeRange') }}</p>
          <section class="rounded-xl border border-default bg-default p-4 sm:p-5">
            <div class="flex flex-wrap justify-between items-center gap-3 mb-6">
              <div>
                <h3 class="font-semibold">{{ t('usageTimeline') }}</h3>
                <p class="text-xs text-muted mt-1">
                  {{
                    t(chartMode === 'line' ? 'usageLineHelp' : 'usageStackHelp', {
                      timezone: usageTimezone,
                    })
                  }}
                </p>
              </div>
              <div class="flex items-center gap-4 flex-wrap">
                <div
                  class="inline-flex rounded-lg border border-default p-1 gap-1"
                  role="group"
                  :aria-label="t('usageChartMode')"
                >
                  <UButton
                    size="xs"
                    :variant="chartMode === 'line' ? 'soft' : 'ghost'"
                    :aria-pressed="chartMode === 'line'"
                    icon="i-lucide-chart-line"
                    @click="chartMode = 'line'"
                    >{{ t('usageLineMode') }}</UButton
                  ><UButton
                    size="xs"
                    :variant="chartMode === 'bar' ? 'soft' : 'ghost'"
                    :aria-pressed="chartMode === 'bar'"
                    icon="i-lucide-chart-column"
                    @click="chartMode = 'bar'"
                    >{{ t('usageBarMode') }}</UButton
                  >
                </div>
                <div class="flex flex-wrap gap-4 text-xs text-muted">
                  <span v-for="key in stackKeys" :key="key" class="inline-flex items-center gap-1.5"
                    ><span class="size-2.5 rounded-sm" :class="stackColors[key]" />{{
                      t(`usageStack_${key}`)
                    }}</span
                  >
                </div>
              </div>
            </div>
            <div v-if="timelineRows.length" class="relative pl-12">
              <div
                class="absolute left-0 top-0 bottom-8 w-10 flex flex-col justify-between text-[10px] text-muted tabular-nums text-right pr-2"
                aria-hidden="true"
              >
                <span v-for="tick in chartTicks" :key="tick.ratio">{{ tick.label }}</span>
              </div>
              <div class="overflow-x-auto pb-1">
                <div
                  class="relative h-56"
                  :style="{ minWidth: `${Math.max(560, timelineRows.length * 36)}px` }"
                >
                  <div
                    class="absolute inset-x-0 top-0 bottom-7 flex flex-col justify-between pointer-events-none"
                    aria-hidden="true"
                  >
                    <div
                      v-for="tick in chartTicks"
                      :key="tick.ratio"
                      class="border-t border-dashed border-default w-full"
                    />
                  </div>
                  <svg
                    v-if="chartMode === 'line'"
                    class="absolute inset-x-0 top-0 w-full h-[calc(100%-28px)] overflow-visible"
                    viewBox="0 0 1000 200"
                    preserveAspectRatio="none"
                    aria-hidden="true"
                  >
                    <g
                      v-for="series in chartLines"
                      :key="series.key"
                      :data-series="series.key"
                      :stroke="lineColors[series.key]"
                      :fill="lineColors[series.key]"
                    >
                      <g v-for="(segment, index) in series.segments" :key="index">
                        <polyline
                          :points="segment.map((p) => `${p.x},${p.y}`).join(' ')"
                          fill="none"
                          stroke-width="2.5"
                          vector-effect="non-scaling-stroke"
                        />
                        <circle
                          v-for="(point, i) in segment"
                          :key="i"
                          :cx="point.x"
                          :cy="point.y"
                          r="2.5"
                          stroke="none"
                        />
                      </g>
                    </g>
                  </svg>
                  <div class="relative flex h-full items-end">
                    <div
                      v-for="point in timelineRows"
                      :key="point.at"
                      class="group flex flex-1 min-w-5 h-full flex-col items-center justify-end"
                    >
                      <div
                        class="relative flex flex-col justify-end w-full max-w-8 h-[calc(100%-28px)] rounded-t overflow-hidden focus-visible:ring-2 focus-visible:ring-primary outline-none"
                        tabindex="0"
                        role="img"
                        :aria-label="timelineTooltip(point)"
                        :title="timelineTooltip(point)"
                      >
                        <template v-if="chartMode === 'bar'"
                          ><div
                            v-for="segment in usageBars(point)"
                            :key="segment.key"
                            :class="stackColors[segment.key]"
                            class="absolute bottom-0 w-full opacity-90"
                            :data-segment="segment.key"
                            :style="{ height: `${(segment.value / timelineMax) * 100}%` }"
                        /></template>
                        <span
                          v-if="!usageBars(point).length"
                          class="text-muted text-center"
                          data-unknown="true"
                          >?</span
                        >
                      </div>
                      <span
                        class="h-7 pt-2 text-[10px] text-muted tabular-nums whitespace-nowrap"
                        >{{ localBucket(point.at, true) }}</span
                      >
                    </div>
                  </div>
                </div>
              </div>
            </div>
            <p v-else class="py-12 text-center text-sm text-muted">{{ t('usageNoData') }}</p>
            <p class="text-[11px] text-muted mt-3">{{ t('usageTimelineHelp') }}</p>
          </section>
          <details
            v-if="timelineRows.length"
            class="rounded-xl border border-default overflow-hidden"
          >
            <summary class="px-4 py-3 text-sm font-medium cursor-pointer">
              {{ t('usageTimeDetails') }}
              <span class="text-muted font-normal ml-2"
                >{{ timelineRows.length }} {{ t('usageBuckets') }}</span
              >
            </summary>
            <div class="overflow-x-auto">
              <table class="model-table w-full text-sm text-left">
                <thead>
                  <tr>
                    <th>{{ t('usageTime') }} ({{ usageTimezone }})</th>
                    <th>{{ t('usageInput') }}</th>
                    <th>{{ t('usageOutput') }}</th>
                    <th>{{ t('usageCacheRead') }}</th>
                    <th>{{ t('usageCacheWrite') }}</th>
                    <th>{{ t('usagePlannerCacheRate') }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="point in timelineRows" :key="point.at">
                    <td>{{ localBucket(point.at) }}</td>
                    <td :title="point.known ? t('usageCounterUnknown') : String(point.input)">
                      {{ point.known ? t('usageCounterUnknown') : compactNumber(point.input) }}
                    </td>
                    <td :title="point.known ? t('usageCounterUnknown') : String(point.output)">
                      {{ point.known ? t('usageCounterUnknown') : compactNumber(point.output) }}
                    </td>
                    <td :title="timelineTooltip(point)" :aria-label="timelineTooltip(point)">
                      {{ point.readKnown ? compactNumber(point.read) : t('usageCounterUnknown') }}
                      ({{ point.readKnown }}/{{ point.calls }})
                    </td>
                    <td :title="timelineTooltip(point)" :aria-label="timelineTooltip(point)">
                      {{ point.writeKnown ? compactNumber(point.write) : t('usageCounterUnknown') }}
                      ({{ point.writeKnown }}/{{ point.calls }})
                    </td>
                    <td>
                      {{ plannerCacheRate(point.planner, plannerTimelineIncomplete) }}
                      <p class="text-xs text-muted">{{ plannerCoverage(point.planner) }}</p>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </details>
        </div>
        <div class="overflow-x-auto">
          <table class="model-table w-full text-left text-sm">
            <thead>
              <tr>
                <th>{{ t('usageDay') }}</th>
                <th>{{ t('mcModelName') }}</th>
                <th>{{ t('mcTask') }}</th>
                <th>{{ t('usageCalls') }}</th>
                <th>{{ t('usageTokens') }}</th>
                <th>{{ t('usageCache') }}</th>
                <th>{{ t('usageDuration') }}</th>
                <th>{{ t('usageCost') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="row in filteredUsage"
                :key="
                  JSON.stringify([row.day, row.modelId, row.providerId, row.apiModelName, row.role])
                "
              >
                <td class="whitespace-nowrap">{{ localTime(row.day) }}</td>
                <td class="min-w-52">
                  <div>{{ modelName(row.modelId) }}</div>
                  <p class="text-xs text-muted mt-1">
                    {{ providerName(row.providerId) }} · {{ row.apiModelName }}
                  </p>
                </td>
                <td>
                  {{
                    Object.hasOwn(roleKeys, row.role)
                      ? roleLabel(row.role as Route['role'])
                      : row.role
                  }}
                </td>
                <td :title="String(row.calls)">{{ compactNumber(row.calls) }}</td>
                <td
                  class="whitespace-nowrap"
                  :title="`${row.inputTokens} / ${row.outputTokens}`"
                  :aria-label="`${t('usageInput')}: ${row.inputTokens}; ${t('usageOutput')}: ${row.outputTokens}`"
                >
                  {{ compactNumber(row.inputTokens) }} / {{ compactNumber(row.outputTokens) }}
                  <p v-if="row.unknownUsageCalls" class="text-xs text-warning">
                    {{ t('usageUnknownCalls', { count: row.unknownUsageCalls, total: row.calls }) }}
                  </p>
                </td>
                <td class="min-w-64">
                  <template
                    v-if="
                      row.cacheKnownCalls || row.cacheReadKnownCalls || row.cacheWriteKnownCalls
                    "
                  >
                    <div v-if="row.cacheKnownCalls" :title="String(row.ordinaryInputTokens)">
                      {{ t('usageOrdinary') }}: {{ compactNumber(row.ordinaryInputTokens) }}
                    </div>
                    <div
                      :title="
                        row.cacheReadKnownCalls
                          ? String(row.cacheReadInputTokens)
                          : t('usageCounterUnknown')
                      "
                    >
                      {{ t('usageCacheRead') }}:
                      {{
                        row.cacheReadKnownCalls
                          ? compactNumber(row.cacheReadInputTokens)
                          : t('usageCounterUnknown')
                      }}
                      <span class="text-xs text-muted"
                        >({{ row.cacheReadKnownCalls }}/{{ row.calls }})</span
                      >
                    </div>
                    <div
                      :title="
                        row.cacheWriteKnownCalls
                          ? String(row.cacheWriteInputTokens)
                          : t('usageCounterUnknown')
                      "
                    >
                      {{ t('usageCacheWrite') }}:
                      {{
                        row.cacheWriteKnownCalls
                          ? compactNumber(row.cacheWriteInputTokens)
                          : t('usageCounterUnknown')
                      }}
                      <span class="text-xs text-muted"
                        >({{ row.cacheWriteKnownCalls }}/{{ row.calls }})</span
                      >
                    </div>
                    <p
                      v-if="
                        row.cacheWrite5mInputTokens ||
                        row.cacheWrite1hInputTokens ||
                        row.cacheWrite30mInputTokens ||
                        row.cacheWrite12hInputTokens
                      "
                      class="text-xs text-muted mt-1"
                    >
                      {{
                        t('usageWriteTtl', {
                          m5: row.cacheWrite5mInputTokens,
                          h1: row.cacheWrite1hInputTokens,
                          m30: row.cacheWrite30mInputTokens,
                          h12: row.cacheWrite12hInputTokens,
                        })
                      }}
                    </p>
                  </template>
                  <div v-else>{{ t('usageCacheUnknown') }}</div>
                  <p class="text-xs text-muted mt-1">
                    {{ t('usageCacheCoverage', { known: row.cacheKnownCalls, total: row.calls }) }}
                  </p>
                </td>
                <td class="whitespace-nowrap">{{ row.durationMs }} ms</td>
                <td class="whitespace-nowrap">
                  <div>{{ cost(row) }}</div>
                  <p
                    v-if="
                      (row.pricedCalls || row.partialPricedCalls) && row.pricedCalls < row.calls
                    "
                    class="text-xs text-warning mt-1"
                  >
                    {{ t('usagePartialCost') }}
                  </p>
                  <p
                    v-if="row.pricedCalls || row.partialPricedCalls"
                    class="text-xs text-muted mt-1 max-w-72 whitespace-normal"
                  >
                    {{
                      t('usageCostBreakdown', {
                        input: usd(row.ordinaryInputUsd),
                        output: usd(row.outputUsd),
                        read: usd(row.cacheReadUsd),
                        write: usd(row.cacheWriteUsd),
                      })
                    }}
                  </p>
                  <p
                    v-if="row.legacyCalls"
                    class="text-xs text-muted mt-1 max-w-72 whitespace-normal"
                  >
                    {{
                      t('usageLegacy', {
                        calls: row.legacyCalls,
                        cost: usd(row.legacyEstimatedUsd),
                      })
                    }}
                  </p>
                  <p class="text-xs text-muted mt-1">
                    {{ t('usagePriced', { priced: row.pricedCalls, total: row.calls }) }} ·
                    {{ t('usagePartialCalls', { count: row.partialPricedCalls }) }}
                  </p>
                </td>
              </tr>
              <tr v-if="!filteredUsage.length">
                <td colspan="8" class="p-8 text-center text-muted">
                  {{ t(usageBusy ? 'usageLoading' : usage.length ? 'uxNoMatches' : 'usageEmpty') }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </section>
    <UModal
      :open="editorOpen"
      :title="editorTitle"
      :description="editorDescription"
      :dismissible="!operationBusy"
      :close="!operationBusy"
      :ui="{
        content: 'max-w-[680px] max-h-[calc(100dvh-3rem)]',
        header: 'p-4 sm:p-6 shrink-0',
        body: 'p-4 sm:p-6 min-h-0 overflow-y-auto',
        footer: 'p-4 sm:p-6 shrink-0 justify-between',
      }"
      @update:open="requestClose"
    >
      <template #body>
        <form id="model-entity-form" class="space-y-5" @submit.prevent="persist()">
          <UAlert v-if="editorError" color="error" :title="editorError" />
          <fieldset :disabled="operationBusy" class="space-y-5 min-w-0">
            <template v-if="selectedProvider">
              <UFormField :label="t('mcProviderName')" required
                ><UInput
                  :model-value="selectedProvider?.name"
                  @update:model-value="(value: string | number) => setProviderField('name', value)"
                  class="w-full"
                  required
              /></UFormField>
              <UFormField :label="t('protocol')" :help="t('mcProtocolHelp')" required
                ><USelect
                  :model-value="selectedProvider?.protocol"
                  @update:model-value="
                    (value: string | number) => setProviderField('protocol', value)
                  "
                  :items="protocolOptions"
                  class="w-full"
              /></UFormField>
              <UFormField :label="t('baseUrl')" required
                ><UInput
                  :model-value="selectedProvider?.baseUrl"
                  @update:model-value="
                    (value: string | number) => setProviderField('baseUrl', value)
                  "
                  type="url"
                  placeholder="https://api.openai.com/v1"
                  class="w-full"
                  required
              /></UFormField>
              <UFormField :label="t('mcCredentialLabel')" :help="t('mcCredentialHelp')">
                <UInput
                  v-model="credentialInput"
                  type="password"
                  autocomplete="new-password"
                  :disabled="credentialClear"
                  :placeholder="
                    selectedProvider?.credentialConfigured
                      ? t('mcCredentialPreserve')
                      : t('mcCredentialEnter')
                  "
                  class="w-full"
                />
                <p class="mt-2 text-sm">
                  {{
                    selectedProvider?.credentialConfigured
                      ? t('mcCredentialStored')
                      : t('mcCredentialMissing')
                  }}
                </p>
                <UCheckbox v-model="credentialClear" :label="t('mcCredentialClear')" class="mt-2" />
              </UFormField>
              <UFormField :label="t('mcCredentialEnvLabel')" :help="t('mcCredentialEnvHelp')">
                <UInput
                  :model-value="selectedProvider?.credentialEnv"
                  @update:model-value="
                    (value: string | number) => setProviderField('credentialEnv', value)
                  "
                  placeholder="OPENAI_API_KEY"
                  pattern="[A-Z][A-Z0-9_]*"
                  class="w-full"
                  autocomplete="off"
                />
              </UFormField>
              <UFormField
                :label="t('mcProviderRetries')"
                :help="t('mcProviderRetriesHelp')"
                required
              >
                <UInput
                  v-if="selectedProvider"
                  v-model.number="selectedProvider.retryCount"
                  type="number"
                  min="0"
                  max="5"
                  step="1"
                  required
                  class="w-full"
                />
              </UFormField>
              <UFormField
                :label="t('mcProviderTimeout')"
                :help="t('mcProviderTimeoutHelp')"
                required
              >
                <UInput
                  v-if="selectedProvider"
                  v-model.number="selectedProvider.timeoutMs"
                  type="number"
                  min="100"
                  max="300000"
                  step="1"
                  required
                  class="w-full"
                />
              </UFormField>
              <fieldset
                v-if="
                  selectedProvider &&
                  ['openai-chat', 'openai-responses'].includes(selectedProvider.protocol)
                "
                class="space-y-3"
              >
                <legend>{{ t('mcImageUpload') }}</legend>
                <p class="text-sm text-muted">{{ t('mcImageUploadHelp') }}</p>
                <UFormField
                  v-for="field in imageUploadFields"
                  :key="field.key"
                  :label="t(field.label)"
                >
                  <UInput
                    :model-value="String(selectedProvider.imageUploadLimits?.[field.key] ?? '')"
                    @update:model-value="
                      (value: string | number) => setImageUploadLimit(field.key, value)
                    "
                    :placeholder="field.placeholder"
                    type="number"
                    min="1"
                    step="1"
                    class="w-full"
                  />
                </UFormField>
              </fieldset>
              <details class="border-t border-default pt-4">
                <summary class="text-sm text-muted cursor-pointer">
                  {{ t('mcInternalDetails') }}
                </summary>
                <p class="mt-3 text-xs font-mono break-all">
                  {{ t('mcProviderId') }}: {{ selectedProvider?.id }}
                </p>
              </details>
              <p v-if="inUseCount" class="text-sm text-muted">
                {{ t('mcProviderInUse', { count: inUseCount }) }}
              </p>
            </template>
            <template v-if="selectedModel">
              <UFormField :label="t('name')" :help="t('mcDisplayNameHelp')" required
                ><UInput v-model="selectedModel.displayName" class="w-full" required
              /></UFormField>
              <UFormField :label="t('provider')" :help="t('mcProviderHelp')" required
                ><USelect
                  v-model="selectedModel.providerId"
                  :items="config.providers.map((p) => ({ label: p.name, value: p.id }))"
                  class="w-full"
              /></UFormField>
              <UFormField :label="t('apiModel')" :help="t('mcApiNameHelp')" required
                ><UInput v-model="selectedModel.apiModelName" class="w-full" required
              /></UFormField>
              <UFormField :label="t('mcPromptCacheMode')" :help="t('mcPromptCacheHelp')"
                ><USelect
                  :model-value="selectedModel.promptCacheMode ?? 'auto'"
                  :items="promptCacheOptions"
                  class="w-full"
                  @update:model-value="setPromptCacheMode"
              /></UFormField>
              <div class="space-y-2">
                <UButton
                  variant="soft"
                  color="neutral"
                  icon="i-lucide-list-restart"
                  :loading="discoveryBusy"
                  :disabled="operationBusy"
                  @click="discover(selectedModel.providerId)"
                  >{{ t('mcDiscovery') }}</UButton
                >
                <p v-if="discoveryError" class="text-sm text-warning">{{ discoveryError }}</p>
                <p v-if="discovered && !discovered.length" class="text-sm text-muted">
                  {{ t('mcDiscoveryEmpty') }}
                </p>
                <USelect
                  v-if="discovered?.length"
                  :model-value="undefined"
                  :items="discovered"
                  :placeholder="t('mcSelectDiscovered')"
                  :aria-label="t('mcSelectDiscovered')"
                  class="w-full"
                  @update:model-value="
                    (value) => {
                      if (selectedModel && value) selectedModel.apiModelName = value
                    }
                  "
                />
              </div>
              <UFormField :label="t('capabilities')" :help="t('mcDeclaredHelp')"
                ><div class="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-3 pt-1">
                  <UCheckbox
                    v-for="cap in capabilities"
                    :key="cap"
                    :id="`model-capability-${cap}`"
                    :aria-label="capabilityLabel(cap)"
                    :ui="{
                      root: 'items-center',
                      container: 'min-h-11 sm:min-h-0',
                      label: 'min-h-11 flex items-center sm:min-h-0',
                    }"
                    :model-value="selectedModel.capabilities.includes(cap)"
                    :label="capabilityLabel(cap)"
                    @update:model-value="(value) => toggleCapability(cap, value)"
                  /></div
              ></UFormField>
              <UFormField
                v-if="selectedModel.capabilities.includes('embedding')"
                :label="t('eiDimensions')"
                :help="t('eiDimensionsHelp')"
              >
                <UInput
                  type="number"
                  min="1"
                  max="16384"
                  step="1"
                  class="w-full"
                  :placeholder="t('eiNative')"
                  :model-value="String(selectedModel.embeddingDimensions ?? '')"
                  @update:model-value="
                    (value) => {
                      if (selectedModel) {
                        if (value === '') delete selectedModel.embeddingDimensions
                        else selectedModel.embeddingDimensions = Number(value)
                      }
                    }
                  "
                />
              </UFormField>
              <UFormField
                v-if="selectedModel.capabilities.includes('embedding')"
                :label="t('mcEmbeddingBatchSize')"
                :help="t('mcEmbeddingBatchHelp')"
              >
                <UInput
                  type="number"
                  min="1"
                  max="64"
                  step="1"
                  class="w-full"
                  :model-value="String(selectedModel.embeddingBatchSize ?? 4)"
                  @update:model-value="
                    (value) => {
                      if (selectedModel) selectedModel.embeddingBatchSize = Number(value)
                    }
                  "
                />
              </UFormField>
              <details class="border-t border-default pt-4">
                <summary class="cursor-pointer text-sm font-medium">{{ t('mcPricing') }}</summary>
                <p class="mt-3 text-xs leading-5 text-muted">{{ t('priceOptionalHelp') }}</p>
                <p class="mt-2 text-xs leading-5 text-muted">{{ t('cachePriceHelp') }}</p>
                <div class="grid sm:grid-cols-2 gap-4 mt-4">
                  <UFormField v-for="item in priceFields" :key="item.field" :label="t(item.label)">
                    <UInput
                      type="number"
                      min="0"
                      step="any"
                      class="w-full"
                      :model-value="String(selectedModel[item.field] ?? '')"
                      @update:model-value="(value) => price(item.field, value)"
                    />
                  </UFormField>
                </div>
              </details>
              <details class="border-t border-default pt-4">
                <summary class="cursor-pointer text-sm font-medium">
                  {{ t('mcValidation') }}
                </summary>
                <div class="mt-4 space-y-3">
                  <p class="text-xs text-muted">{{ t('probeHelp') }}</p>
                  <p v-if="dirty || isNew" class="text-sm text-muted">
                    {{ t('mcSaveBeforeVerify') }}
                  </p>
                  <div class="flex flex-wrap gap-3">
                    <USelect
                      v-model="probeCapability"
                      :items="
                        capabilities.map((cap) => ({ value: cap, label: capabilityLabel(cap) }))
                      "
                      :aria-label="t('mcValidation')"
                      class="w-full sm:w-48"
                    /><UButton
                      variant="soft"
                      :disabled="dirty || isNew || operationBusy"
                      @click="pendingProbe = true"
                      >{{ t('mcVerify') }}</UButton
                    >
                  </div>
                  <div v-if="pendingProbe" class="border border-warning rounded-md p-4 space-y-3">
                    <p class="text-sm">{{ t('probeChargeWarning') }}</p>
                    <p class="text-sm font-medium">
                      {{ selectedModel.displayName }} · {{ capabilityLabel(probeCapability) }}
                    </p>
                    <div class="flex gap-2">
                      <UButton :disabled="dirty || operationBusy" @click="runProbe">{{
                        t('probeConfirm')
                      }}</UButton
                      ><UButton variant="ghost" color="neutral" @click="pendingProbe = false">{{
                        t('cancel')
                      }}</UButton>
                    </div>
                  </div>
                  <UAlert
                    v-if="probeResults[selectedModel.id] && !dirty"
                    :color="probeResults[selectedModel.id]?.ok ? 'success' : 'warning'"
                    :title="
                      t('mcProbeResult', {
                        capability: capabilityLabel(probeResults[selectedModel.id]!.capability),
                        result: t(
                          probeResults[selectedModel.id]?.ok ? 'probePassed' : 'probeFailed',
                        ),
                      })
                    "
                    :description="`${probeResults[selectedModel.id]?.detail} (${probeResults[selectedModel.id]?.durationMs} ms)`"
                  />
                </div>
              </details>
              <details class="border-t border-default pt-4">
                <summary class="text-sm text-muted cursor-pointer">
                  {{ t('mcInternalDetails') }}
                </summary>
                <p class="mt-3 text-xs font-mono break-all">
                  {{ t('mcInstanceId') }}: {{ selectedModel.id }}
                </p>
              </details>
            </template>
            <template v-if="selectedRoute">
              <p class="text-sm text-muted">
                {{ roleHelp(selectedRoute.role) }} {{ t('mcAssignmentHelp') }}
              </p>
              <ol class="divide-y divide-default border border-default rounded-lg">
                <li
                  v-for="(id, index) in selectedRoute.orderedModelIds"
                  :key="id"
                  class="p-3 flex gap-3 items-center"
                >
                  <span class="text-xs text-muted w-16 shrink-0">{{
                    index ? t('mcFallback', { number: index }) : t('mcPrimary')
                  }}</span
                  ><span class="flex-1 min-w-0 break-words text-sm">{{ modelChoice(id) }}</span>
                  <div class="flex shrink-0">
                    <UButton
                      icon="i-lucide-arrow-up"
                      variant="ghost"
                      color="neutral"
                      :aria-label="t('moveUp')"
                      :disabled="index === 0"
                      @click="move(index, -1)"
                    /><UButton
                      icon="i-lucide-arrow-down"
                      variant="ghost"
                      color="neutral"
                      :aria-label="t('moveDown')"
                      :disabled="index === selectedRoute.orderedModelIds.length - 1"
                      @click="move(index, 1)"
                    /><UButton
                      icon="i-lucide-x"
                      variant="ghost"
                      color="neutral"
                      :aria-label="t('remove')"
                      @click="selectedRoute.orderedModelIds.splice(index, 1)"
                    />
                  </div>
                </li>
                <li v-if="!selectedRoute.orderedModelIds.length" class="p-4 text-sm text-muted">
                  {{ t('uxUnassigned') }}
                </li>
              </ol>
              <USelect
                :model-value="undefined"
                :placeholder="t('mcAddFallback')"
                :aria-label="t('mcAddFallback')"
                :items="
                  config.models
                    .filter((m) => !selectedRoute?.orderedModelIds.includes(m.id))
                    .map((m) => ({ value: m.id, label: modelChoice(m.id) }))
                "
                class="w-full"
                @update:model-value="
                  (value) => {
                    if (selectedRoute && value) selectedRoute.orderedModelIds.push(value)
                  }
                "
              />
              <details class="border-t border-default pt-4">
                <summary class="cursor-pointer text-sm font-medium">
                  {{ t('mcAdvanced') }}
                </summary>
                <UFormField :label="t('mcTimeout')" :help="t('mcRouteBudgetHelp')" class="mt-4"
                  ><UInput
                    v-model.number="selectedRoute.timeoutMs"
                    type="number"
                    min="100"
                    max="300000"
                    step="1"
                    required
                    class="w-full"
                /></UFormField>
              </details>
            </template>
          </fieldset>
        </form>
      </template>
      <template #footer
        ><div>
          <UButton
            v-if="!isNew && editorKind !== 'route'"
            color="error"
            variant="ghost"
            :disabled="operationBusy || (editorKind === 'provider' && inUseCount > 0)"
            @click="confirmation = 'delete'"
            >{{ t('remove') }}</UButton
          >
        </div>
        <div class="flex gap-3">
          <UButton
            variant="outline"
            color="neutral"
            :disabled="operationBusy"
            @click="requestClose()"
            >{{ t('cancel') }}</UButton
          ><UButton
            type="submit"
            form="model-entity-form"
            :loading="busy"
            :disabled="operationBusy || (!dirty && !isNew)"
            >{{ saveLabel }}</UButton
          >
        </div></template
      >
    </UModal>
    <UModal
      :open="!!confirmation"
      :title="
        confirmationKind === 'discard'
          ? t('mcDiscardTitle')
          : editorKind === 'model'
            ? t('mcDeleteModelTitle', { name: selectedModel?.displayName })
            : t('mcDeleteProviderTitle', { name: selectedProvider?.name })
      "
      :description="
        confirmationKind === 'discard'
          ? t('mcDiscardHelp')
          : editorKind === 'model'
            ? t('mcDeleteModelHelp', { count: affectedRoles })
            : t('mcDeleteProviderHelp')
      "
      :dismissible="!busy"
      :close="!busy"
      @update:open="
        (open) => {
          if (!open && !busy) cancelConfirmation()
        }
      "
    >
      <template #footer
        ><div class="flex justify-end gap-3 w-full">
          <UButton
            variant="outline"
            color="neutral"
            :disabled="busy || !confirmation"
            @click="cancelConfirmation"
            >{{ t(confirmationKind === 'discard' ? 'mcKeepEditing' : 'cancel') }}</UButton
          ><UButton
            color="error"
            :loading="busy"
            :disabled="busy || !confirmation"
            @click="confirmationKind === 'discard' ? discardEditor() : persist(true)"
            >{{ t(confirmationKind === 'discard' ? 'mcDiscard' : 'remove') }}</UButton
          >
        </div></template
      >
    </UModal>
  </div>
</template>
<style scoped>
.usage-stat {
  border: 1px solid var(--ui-border);
  border-radius: 12px;
  padding: 20px;
  background: var(--ui-bg);
}
.usage-stat-label {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--ui-text-muted);
}
.usage-stat-value {
  display: block;
  margin-top: 12px;
  font-size: 26px;
  line-height: 1.2;
  font-weight: 650;
  letter-spacing: -0.04em;
  font-variant-numeric: tabular-nums;
}
.usage-stat-note {
  margin-top: 8px;
  font-size: 11px;
  color: var(--ui-text-muted);
}

.model-table th {
  height: 44px;
  padding: 0 16px;
  font-size: 12px;
  font-weight: 500;
  color: var(--ui-text-muted);
  background: var(--ui-bg-muted);
}
.model-table td {
  padding: 12px 16px;
  vertical-align: top;
  line-height: 20px;
  border-top: 1px solid var(--ui-border);
}
.model-table tbody tr:hover {
  background: color-mix(in srgb, var(--ui-bg-muted) 45%, transparent);
}
.model-table .action-cell {
  vertical-align: middle;
  text-align: right;
}
</style>
