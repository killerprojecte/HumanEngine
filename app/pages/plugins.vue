<script setup lang="ts">
import {
  getSnowLumaIssues,
  isSnowLumaIssue,
  type SnowLumaIssue,
} from '../../src/plugins/builtin/snowluma/validation'
import {
  getWebSearchIssues,
  isWebSearchIssue,
  type WebSearchIssue,
} from '../../src/plugins/builtin/web-search/validation'
import { isPluginStartupErrorCode } from '../../src/plugin-errors'
import type { components } from '~/types/api'
type Plugin = components['schemas']['Plugin']
type Field = {
  key: string
  title: string
  description: string
  type: string
  required: boolean
  secret: boolean
  group: string
  options?: Array<{ label: string; value: string }>
  minimum?: number
  maximum?: number
  defaultValue?: unknown
}
const { t } = useI18n(),
  { client } = useApi()
const plugins = ref<Plugin[]>([]),
  selectedId = ref<string>(),
  values = ref<Record<string, string | number | boolean>>({}),
  original = ref<Record<string, string | number | boolean>>({})
const busy = ref(false),
  loading = ref(false),
  error = ref(''),
  saved = ref(false)
const validationShown = ref(false)
const serverIssues = ref<Array<SnowLumaIssue | WebSearchIssue>>([])
function snowValidationValues() {
  const result: Record<string, unknown> = { ...values.value }
  for (const field of fields.value) {
    if (result[field.key] === '') delete result[field.key]
    else if (
      result[field.key] !== undefined &&
      (field.type === 'integer' || field.type === 'number')
    )
      result[field.key] = Number(result[field.key])
  }
  return result
}
const fieldIssues = computed(() => {
  if (selected.value?.id === 'web-search')
    return [
      ...(validationShown.value ? getWebSearchIssues(snowValidationValues()) : []),
      ...serverIssues.value,
    ]
  if (selected.value?.id !== 'snowluma') return []
  return [
    ...(validationShown.value ? getSnowLumaIssues(snowValidationValues()) : []),
    ...serverIssues.value,
  ]
})
function fieldError(key: string) {
  const issue = fieldIssues.value.find((entry) => entry.field === key)
  return issue ? t(`pgValidation.${issue.reason}`) : undefined
}
function readValidationIssues(response: unknown) {
  const issues = object(response).issues
  return Array.isArray(issues)
    ? issues.filter((issue) => isSnowLumaIssue(issue) || isWebSearchIssue(issue))
    : []
}
function diagnosticMessage(code: string) {
  if (selectedId.value === 'web-search' && code === 'credential_not_configured')
    return t('pgWebRequiredKeyMissing')
  return isPluginStartupErrorCode(code) ? t(`pluginStartupErrors.${code}`) : code
}
function diagnosticResponse(value: {
  error: string
  requestId?: string
  systemCode?: string
  httpStatus?: number
}) {
  return (
    diagnosticMessage(value.error) +
    (value.systemCode ? ` [${value.systemCode}]` : '') +
    (value.httpStatus ? ` [HTTP ${value.httpStatus}]` : '') +
    (value.requestId ? ` (${t('pgDiagnosticRef')}: ${value.requestId})` : '')
  )
}
const selected = computed(() => plugins.value.find((plugin) => plugin.id === selectedId.value))
function object(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {}
}
const builtinFields: Record<string, Record<string, string>> = {
  snowluma: {
    transport: 'pgTransport',
    url: 'pgSnowUrl',
    accountId: 'pgSnowAccount',
    credentialEnv: 'pgSnowCredential',
    token: 'pgSnowToken',
    clearToken: 'pgClearSnowToken',
    eventToken: 'pgSnowEventToken',
    clearEventToken: 'pgClearSnowEventToken',
    eventHost: 'pgEventHost',
    eventPort: 'pgEventPort',
    eventPath: 'pgEventPath',
    eventCredentialEnv: 'pgEventCredential',
  },
  'typing-delay': { speedFactor: 'pgTypingSpeed' },
  'web-search': {
    provider: 'pgWebProvider',
    fallbackProvider: 'pgWebFallback',
    allowExperimentalHtml: 'pgWebHtml',
    maxResults: 'pgWebResults',
    timeoutMs: 'pgWebTimeout',
    maxResponseBytes: 'pgWebBytes',
    maxConcurrency: 'pgWebConcurrency',
    tavilyKey: 'pgWebTavilyKey',
    clearTavilyKey: 'pgWebClearTavily',
    youKey: 'pgWebYouKey',
    clearYouKey: 'pgWebClearYou',
    deepseekKey: 'pgWebDeepseekKey',
    clearDeepseekKey: 'pgWebClearDeepseek',
  },
}
function builtinFieldTitle(id: string | undefined, key: string) {
  const label = id ? builtinFields[id]?.[key] : undefined
  return label ? t(label) : undefined
}
function builtinFieldHelp(id: string | undefined, key: string) {
  if (id === 'web-search') {
    if (key === 'provider') return t('pgWebProviderHelp')
    if (key === 'fallbackProvider') return t('pgWebFallbackHelp')
    if (key === 'allowExperimentalHtml') return t('pgWebHtmlHelp')
    if (key.endsWith('Key')) return t('pgWebKeyHelp')
  }
  if (id === 'typing-delay' && key === 'speedFactor') return t('pgTypingSpeedHelp')
  if (id === 'snowluma' && key === 'url')
    return t(values.value.transport === 'http' ? 'pgSnowHttpUrlHelp' : 'pgSnowWsUrlHelp')
  if (id === 'snowluma' && key === 'eventToken') return t('pgEventHelp')
  if (id === 'snowluma' && key === 'token') return t('pgSnowTokenHelp')
  if (id === 'snowluma' && key === 'accountId') return t('pgSnowAccountHelp')
  if (id === 'snowluma' && key.endsWith('Env')) return t('pgLegacyCredentialHelp')
  if (id === 'snowluma' && key === 'eventPort') return ''
  if (id === 'snowluma' && key === 'transport') return t('pgTransportHelp')
}
function pluginName(plugin: Plugin) {
  if (plugin.builtin && plugin.id === 'web-search') return t('pgWebName')
  return plugin.builtin && plugin.id === 'typing-delay' ? t('pgTypingName') : plugin.name
}
function pluginDescription(plugin: Plugin) {
  if (plugin.builtin && plugin.id === 'web-search') return t('pgWebDescription')
  return plugin.builtin && plugin.id === 'typing-delay'
    ? t('pgTypingDescription')
    : plugin.builtin && plugin.id === 'snowluma'
      ? t('pgSnowDescription')
      : plugin.description
}
const fields = computed<Field[]>(() => {
  const schema = selected.value?.configSchema ?? {},
    required = Array.isArray(schema.required) ? schema.required : []
  return Object.entries(object(schema.properties)).map(([key, raw]) => {
    const entry = object(raw)
    return {
      key,
      title:
        builtinFieldTitle(selected.value?.id, key) ??
        (typeof entry.title === 'string' ? entry.title : key),
      description:
        builtinFieldHelp(selected.value?.id, key) ??
        (typeof entry.description === 'string' ? entry.description : ''),
      type: typeof entry.type === 'string' ? entry.type : 'string',
      required:
        required.includes(key) ||
        (selected.value?.id === 'snowluma' &&
          values.value.transport === 'http' &&
          key === 'eventPort'),
      secret: entry.secret === true,
      group:
        selected.value?.id === 'snowluma' && key.endsWith('Env')
          ? t('pgLegacyCredentialGroup')
          : typeof entry.group === 'string'
            ? entry.group
            : t('pluginGeneral'),
      ...(Array.isArray(entry.enum)
        ? { options: entry.enum.map((value) => ({ label: String(value), value: String(value) })) }
        : {}),
      minimum: typeof entry.minimum === 'number' ? entry.minimum : undefined,
      maximum: typeof entry.maximum === 'number' ? entry.maximum : undefined,
      defaultValue: entry.default,
    }
  })
})
function httpOnlyField(key: string) {
  return key.startsWith('event') || key === 'clearEventToken'
}
const visibleFields = computed(() =>
  fields.value.filter(
    (field) =>
      selected.value?.id !== 'snowluma' ||
      !httpOnlyField(field.key) ||
      values.value.transport === 'http',
  ),
)
const groups = computed(() => [...new Set(visibleFields.value.map((field) => field.group))])
const dirty = computed(
  () => !!selected.value && JSON.stringify(values.value) !== JSON.stringify(original.value),
)
const safePages = computed(() =>
  (selected.value?.pages ?? []).filter(
    (page) =>
      page.entry.startsWith(`/plugins/${selected.value?.id}/`) &&
      /^\/[a-zA-Z0-9/_\-.]+$/.test(page.entry) &&
      !page.entry.includes('..') &&
      !page.entry.includes('//'),
  ),
)
function setValue(key: string, value: unknown) {
  serverIssues.value = serverIssues.value.filter((issue) => issue.field !== key)
  if (key === 'transport') serverIssues.value = []
  if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean')
    values.value[key] = value
}
function formValues(config: Record<string, unknown>) {
  const result: Record<string, string | number | boolean> = {}
  for (const field of fields.value) {
    const value = config[field.key] ?? field.defaultValue
    if (field.secret) result[field.key] = ''
    else if (field.type === 'boolean') result[field.key] = value === true
    else if (field.type === 'object' || field.type === 'array')
      result[field.key] = value === undefined ? '' : JSON.stringify(value, null, 2)
    else
      result[field.key] =
        typeof value === 'string' || typeof value === 'number' ? String(value) : ''
  }
  return result
}
const query = ref(''),
  configReady = ref(false),
  uncertain = ref(false),
  reconciled = ref(false)
const freshValues = ref<Record<string, string | number | boolean>>()
const visiblePlugins = computed(() =>
  plugins.value.filter((p) =>
    `${pluginName(p)} ${pluginDescription(p)} ${p.name} ${p.description} ${p.id}`
      .toLowerCase()
      .includes(query.value.trim().toLowerCase()),
  ),
)
const blocked = computed(() => busy.value || loading.value || uncertain.value)
const confirmation = ref(false),
  confirmationKind = ref<'discard' | 'enable' | 'disable' | 'reload'>('discard')
const confirmationCopy = ref({ title: '', help: '', action: '' })
let nextSelection: string | undefined, pendingNavigation: ((allow: boolean) => void) | undefined
function snapshot() {
  return JSON.parse(JSON.stringify(values.value)) as Record<string, string | number | boolean>
}
async function list() {
  const response = await client.GET('/api/plugins')
  if (!response.data)
    throw new Error(response.error ? diagnosticResponse(response.error) : t('networkError'))
  plugins.value = response.data
}
async function load(recovery = false) {
  if (busy.value || loading.value || (dirty.value && !recovery)) return
  loading.value = true
  error.value = ''
  try {
    await list()
    if (recovery && selectedId.value) {
      const response = await client.GET('/api/plugins/{id}/config', {
        params: { path: { id: selectedId.value } },
      })
      if (!response.data)
        throw new Error(response.error ? diagnosticResponse(response.error) : t('networkError'))
      freshValues.value = formValues(response.data)
      reconciled.value = true
    }
  } catch (cause) {
    error.value = cause instanceof Error ? diagnosticMessage(cause.message) : t('networkError')
  } finally {
    loading.value = false
  }
}
async function select(id: string) {
  if (blocked.value) return
  validationShown.value = false
  serverIssues.value = []
  loading.value = true
  error.value = ''
  saved.value = false
  configReady.value = false
  selectedId.value = id
  values.value = {}
  original.value = {}
  try {
    const response = await client.GET('/api/plugins/{id}/config', { params: { path: { id } } })
    if (!response.data)
      throw new Error(response.error ? diagnosticResponse(response.error) : t('networkError'))
    values.value = formValues(response.data)
    original.value = snapshot()
    configReady.value = true
  } catch (cause) {
    error.value = cause instanceof Error ? diagnosticMessage(cause.message) : t('networkError')
  } finally {
    loading.value = false
  }
}
function ask(kind: typeof confirmationKind.value) {
  if (busy.value || loading.value || (kind !== 'discard' && (uncertain.value || dirty.value)))
    return
  if (kind === 'enable' && selected.value?.id === 'web-search') {
    validationShown.value = true
    if (fieldIssues.value.length) {
      error.value = t('pgValidationSummary')
      return
    }
  }
  confirmationKind.value = kind
  confirmationCopy.value = {
    title: t(kind === 'discard' ? 'pgDiscardTitle' : 'pgLifecycleTitle', {
      name: selected.value ? pluginName(selected.value) : '',
    }),
    help: t(
      kind === 'discard'
        ? uncertain.value
          ? 'pgDiscardUncertain'
          : 'pgDiscardHelp'
        : kind === 'reload'
          ? 'pgReloadHelp'
          : kind === 'disable'
            ? 'pgDisableHelp'
            : 'pgEnableHelp',
    ),
    action: t(
      kind === 'discard'
        ? 'resetChanges'
        : kind === 'reload'
          ? 'pluginReload'
          : kind === 'disable'
            ? 'disable'
            : 'enable',
    ),
  }
  confirmation.value = true
}
function choose(id: string) {
  if (busy.value || loading.value || id === selectedId.value) return
  if (dirty.value || uncertain.value) {
    nextSelection = id
    ask('discard')
  } else void select(id)
}
function cancelConfirmation() {
  if (busy.value) return
  confirmation.value = false
  nextSelection = undefined
  pendingNavigation?.(false)
  pendingNavigation = undefined
}
function reset() {
  if (busy.value || loading.value) return
  validationShown.value = false
  serverIssues.value = []
  if (uncertain.value && !reconciled.value && !pendingNavigation) return
  if (pendingNavigation) {
    confirmation.value = false
    pendingNavigation(true)
    pendingNavigation = undefined
    return
  }
  values.value = freshValues.value
    ? JSON.parse(JSON.stringify(freshValues.value))
    : JSON.parse(JSON.stringify(original.value))
  original.value = snapshot()
  freshValues.value = undefined
  uncertain.value = false
  reconciled.value = false
  confirmation.value = false
  error.value = ''
  saved.value = false
  if (nextSelection) {
    const id = nextSelection
    nextSelection = undefined
    void select(id)
  }
}
function configBody(): Record<string, unknown> {
  const body: Record<string, unknown> = {}
  for (const field of fields.value) {
    // Hidden HTTP settings are not submitted or validated in WebSocket mode.
    // Stored event secrets remain unchanged because omitted secrets mean preserve.
    if (
      selected.value?.id === 'snowluma' &&
      httpOnlyField(field.key) &&
      values.value.transport !== 'http'
    )
      continue
    const value = values.value[field.key]
    if (value === '' || value === undefined) {
      if (field.secret) continue
      if (field.required) throw new Error(t('pluginRequired', { field: field.title }))
      continue
    }
    if (field.options) {
      const raw = object(object(selected.value?.configSchema.properties)[field.key])
        .enum as unknown[]
      body[field.key] = raw.find((option) => String(option) === String(value))
    } else if (field.type === 'number' || field.type === 'integer') {
      const number = Number(value)
      if (
        !Number.isFinite(number) ||
        (field.type === 'integer' && !Number.isInteger(number)) ||
        (field.minimum !== undefined && number < field.minimum) ||
        (field.maximum !== undefined && number > field.maximum)
      )
        throw new Error(t('pluginInvalidField', { field: field.title }))
      body[field.key] = number
    } else if (field.type === 'array' || field.type === 'object') {
      try {
        body[field.key] = JSON.parse(String(value))
      } catch {
        throw new Error(t('pluginInvalidJson', { field: field.title }))
      }
    } else body[field.key] = value
  }
  return body
}
async function save() {
  if (blocked.value || !selected.value || !configReady.value || !dirty.value) return
  serverIssues.value = []
  validationShown.value = true
  if (fieldIssues.value.length) {
    error.value = t('pgValidationSummary')
    return
  }
  let body: Record<string, unknown>
  try {
    body = configBody()
  } catch (cause) {
    error.value = cause instanceof Error ? diagnosticMessage(cause.message) : t('networkError')
    return
  }
  const id = selected.value.id
  busy.value = true
  error.value = ''
  saved.value = false
  let submitted = false,
    metadataPending = false
  try {
    const latest = await client.GET('/api/plugins/{id}/config', { params: { path: { id } } })
    if (!latest.data) throw new Error(latest.error?.error ?? t('networkError'))
    if (JSON.stringify(formValues(latest.data)) !== JSON.stringify(original.value)) {
      freshValues.value = formValues(latest.data)
      uncertain.value = true
      reconciled.value = true
      throw new Error(t('pgConflict'))
    }
    // Preserve the latest unrendered fields allowed by extensible plugin schemas.
    const merged: Record<string, unknown> = { ...latest.data }
    for (const field of fields.value) delete merged[field.key]
    Object.assign(merged, body)
    submitted = true
    const response = await client.PUT('/api/plugins/{id}/config', {
      params: { path: { id } },
      body: merged,
    })
    if (!response.data) {
      serverIssues.value = readValidationIssues(response.error)
      if (serverIssues.value.length) {
        submitted = false
        throw new Error(t('pgValidationSummary'))
      }
      if (response.response.status >= 400 && response.response.status < 500) submitted = false
      throw new Error(response.error ? diagnosticResponse(response.error) : t('networkError'))
    }
    submitted = false
    validationShown.value = false
    serverIssues.value = []
    values.value = formValues(response.data)
    original.value = snapshot()
    saved.value = true
    metadataPending = true
    await list()
    metadataPending = false
  } catch (cause) {
    error.value = cause instanceof Error ? diagnosticMessage(cause.message) : t('networkError')
    if (submitted || metadataPending) {
      uncertain.value = true
      reconciled.value = false
    }
  } finally {
    busy.value = false
  }
}
async function lifecycle() {
  if (!confirmation.value || blocked.value || dirty.value || !selected.value) return
  const id = selected.value.id,
    kind = confirmationKind.value
  if (kind === 'discard') return
  busy.value = true
  error.value = ''
  saved.value = false
  let submitted = false,
    acknowledged = false
  async function change(enabled: boolean) {
    submitted = true
    const response = await client.PUT('/api/plugins/{id}/enabled', {
      params: { path: { id } },
      body: { enabled },
    })
    if (!response.data) {
      serverIssues.value = readValidationIssues(response.error)
      if (serverIssues.value.length) {
        submitted = false
        validationShown.value = true
        throw new Error(t('pgValidationSummary'))
      }
      if (
        (response.response.status >= 400 && response.response.status < 500) ||
        isPluginStartupErrorCode(response.error?.error)
      )
        submitted = false
      throw new Error(response.error ? diagnosticResponse(response.error) : t('networkError'))
    }
    submitted = false
    acknowledged = true
  }
  try {
    if (kind === 'reload') await change(false)
    await change(kind !== 'disable')
    confirmation.value = false
    await list()
  } catch (cause) {
    error.value = cause instanceof Error ? diagnosticMessage(cause.message) : t('networkError')
    if (submitted) {
      uncertain.value = true
      reconciled.value = false
    }
    confirmation.value = false
    try {
      await list()
    } catch {
      if (acknowledged || !submitted) {
        uncertain.value = true
        reconciled.value = false
      }
      /* Keep the operation error and last known state until explicit reconciliation. */
    }
  } finally {
    busy.value = false
  }
}
function confirm() {
  if (!confirmation.value || busy.value || loading.value) return
  if (confirmationKind.value === 'discard') reset()
  else void lifecycle()
}
onBeforeRouteLeave(() => {
  if (busy.value || loading.value) return false
  if (!dirty.value && !uncertain.value) return true
  ask('discard')
  return new Promise<boolean>((resolve) => {
    pendingNavigation?.(false)
    pendingNavigation = resolve
  })
})
function beforeUnload(event: BeforeUnloadEvent) {
  if (!dirty.value && !uncertain.value && !busy.value) return
  event.preventDefault()
  event.returnValue = ''
}
onMounted(() => {
  void load()
  window.addEventListener('beforeunload', beforeUnload)
})
onBeforeUnmount(() => {
  pendingNavigation?.(false)
  window.removeEventListener('beforeunload', beforeUnload)
})
const capabilityKeys: Record<string, string> = {
  adapter: 'pgAdapter',
  'model-protocol': 'pgProtocol',
  history: 'pgHistory',
  recall: 'pgRecall',
  'schema-form': 'pgSchema',
  'send-delay': 'pgTypingName',
  protocol: 'pgProtocol',
  tool: 'pgTools',
  tools: 'pgTools',
  hooks: 'pgHooks',
  pages: 'pluginCustomPages',
}
const capabilityLabel = (name: string) => (capabilityKeys[name] ? t(capabilityKeys[name]) : name)
</script>
<template>
  <div class="space-y-5">
    <header class="flex justify-between items-start gap-4">
      <div>
        <h1 class="page-heading">{{ t('pluginsTitle') }}</h1>
        <p class="text-sm text-muted mt-2">{{ t('pgIntro') }}</p>
      </div>
      <UButton
        class="shrink-0 whitespace-nowrap"
        color="neutral"
        variant="outline"
        icon="i-lucide-refresh-cw"
        :loading="loading"
        :disabled="busy || loading || dirty || uncertain"
        @click="load()"
        >{{ t('refresh') }}</UButton
      >
    </header>
    <p class="text-sm text-muted rounded-md border border-default px-4 py-3">
      {{ t('pluginsTrust') }}
    </p>
    <UAlert v-if="error" color="error" :title="error" /><UAlert
      v-if="saved && !dirty"
      color="success"
      :title="t('pgSaved')"
    />
    <section v-if="uncertain" class="rounded-lg border border-warning/40 p-4 space-y-3">
      <p class="text-sm leading-6">{{ t(reconciled ? 'pgReconciled' : 'pgUncertain') }}</p>
      <div class="flex flex-wrap gap-2">
        <UButton
          color="neutral"
          variant="outline"
          :disabled="busy || loading"
          @click="load(true)"
          >{{ t('pgReadSaved') }}</UButton
        ><UButton v-if="reconciled" :disabled="busy || loading" @click="ask('discard')">{{
          t('pgUseSaved')
        }}</UButton>
      </div>
    </section>
    <div class="grid lg:grid-cols-[260px_minmax(0,1fr)] gap-5 items-start">
      <aside class="rounded-lg border border-default bg-default overflow-hidden">
        <div class="p-3 border-b border-default">
          <UInput
            v-model="query"
            icon="i-lucide-search"
            :placeholder="t('pgSearch')"
            :aria-label="t('pgSearch')"
            class="w-full"
          />
        </div>
        <button
          v-for="plugin in visiblePlugins"
          :key="plugin.id"
          type="button"
          class="plugin-choice w-full text-left p-4 border-b border-default last:border-0"
          :class="
            selectedId === plugin.id
              ? 'bg-primary/5 border-l-2 border-l-primary'
              : 'hover:bg-elevated/50'
          "
          :aria-pressed="selectedId === plugin.id"
          :disabled="busy || loading"
          @click="choose(plugin.id)"
        >
          <span class="flex items-start justify-between gap-2"
            ><span class="font-medium break-words">{{ pluginName(plugin) }}</span
            ><span
              class="text-xs shrink-0"
              :class="plugin.error ? 'text-error' : plugin.enabled ? 'text-success' : 'text-muted'"
              >{{
                t(plugin.error ? 'pgErrorState' : plugin.enabled ? 'enabled' : 'disabled')
              }}</span
            ></span
          ><span class="block text-xs text-muted mt-2 leading-5 line-clamp-2">{{
            pluginDescription(plugin)
          }}</span
          ><span class="block text-xs text-muted mt-2"
            >v{{ plugin.version }} · {{ t(plugin.builtin ? 'pgBuiltin' : 'pgLocal') }}</span
          >
        </button>
        <p v-if="!visiblePlugins.length" class="p-4 text-sm text-muted">
          {{ t(loading ? 'uxLoading' : plugins.length ? 'uxNoMatches' : 'pluginsEmpty') }}
        </p>
      </aside>
      <section v-if="selected" class="min-w-0 rounded-lg border border-default bg-default">
        <header class="p-5 border-b border-default space-y-3">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h2 class="text-lg font-semibold break-words">{{ pluginName(selected) }}</h2>
              <p class="mt-1 text-xs text-muted break-all">
                {{ selected.id }} · v{{ selected.version }}
              </p>
            </div>
            <div class="flex flex-wrap gap-2">
              <UButton
                color="neutral"
                variant="outline"
                size="sm"
                :disabled="blocked || dirty"
                @click="ask(selected.enabled ? 'disable' : 'enable')"
                >{{ t(selected.enabled ? 'disable' : 'enable') }}</UButton
              ><UButton
                v-if="selected.enabled"
                color="neutral"
                variant="outline"
                size="sm"
                :disabled="blocked || dirty"
                @click="ask('reload')"
                >{{ t('pluginReload') }}</UButton
              >
            </div>
          </div>
          <p class="text-sm text-muted">{{ pluginDescription(selected) }}</p>
          <p class="text-xs text-muted">{{ t('pgStatusHelp') }}</p>
          <div class="flex flex-wrap gap-2">
            <UBadge
              v-for="capability in selected.capabilities"
              :key="capability"
              variant="outline"
              color="neutral"
              >{{ capabilityLabel(capability) }}</UBadge
            >
          </div>
          <UAlert
            v-if="selected.error"
            color="error"
            :title="
              diagnosticResponse({ error: selected.error, requestId: selected.errorRequestId })
            "
          />
        </header>
        <div v-if="!configReady" class="p-5 text-sm text-muted">
          <p>{{ t(loading ? 'uxLoading' : 'pgConfigUnavailable') }}</p>
          <UButton v-if="!loading" class="mt-3" @click="select(selected.id)">{{
            t('refresh')
          }}</UButton>
        </div>
        <form v-else class="p-5 space-y-5" @submit.prevent="save">
          <div>
            <h3 class="font-semibold">{{ t('configure') }}</h3>
            <p class="text-sm text-muted mt-2 leading-6">{{ t('pgSaveHelp') }}</p>
            <p v-if="dirty" class="mt-2 text-sm text-primary">{{ t('pgDraftHelp') }}</p>
          </div>
          <fieldset
            v-for="group in groups"
            :key="group"
            :disabled="blocked"
            class="space-y-4 min-w-0 border-t border-default pt-4"
          >
            <legend class="font-medium text-sm pr-2">{{ group }}</legend>
            <UFormField
              v-for="field in visibleFields.filter((item) => item.group === group)"
              :key="field.key"
              :label="field.title"
              :description="field.description"
              :required="field.required"
              :error="fieldError(field.key)"
              :help="
                field.secret
                  ? t(
                      selected.configuredSecrets[field.key]
                        ? 'pgCredentialStored'
                        : selected.id === 'web-search'
                          ? 'pgWebKeyMissing'
                          : 'pgCredentialMissing',
                    )
                  : field.key === 'credentialEnv'
                    ? t('pgCredentialHelp')
                    : undefined
              "
            >
              <USelect
                v-if="field.options"
                :model-value="String(values[field.key] ?? '')"
                :items="field.options"
                class="w-full"
                @update:model-value="(value) => setValue(field.key, value)"
              />
              <USwitch
                v-else-if="field.type === 'boolean'"
                :id="`plugin-field-${field.key}`"
                :aria-label="field.title"
                :model-value="values[field.key] === true"
                @update:model-value="(value) => setValue(field.key, value)"
              />
              <UTextarea
                v-else-if="field.type === 'object' || field.type === 'array'"
                :model-value="String(values[field.key] ?? '')"
                :rows="5"
                class="w-full font-mono"
                @update:model-value="(value) => setValue(field.key, value)"
              />
              <UInput
                v-else
                :model-value="String(values[field.key] ?? '')"
                :type="
                  field.secret
                    ? 'password'
                    : field.type === 'number' || field.type === 'integer'
                      ? 'number'
                      : 'text'
                "
                :min="field.minimum"
                :max="field.maximum"
                :step="field.type === 'integer' ? 1 : 'any'"
                autocomplete="off"
                class="w-full"
                @update:model-value="(value) => setValue(field.key, value)"
              />
            </UFormField>
          </fieldset>
          <p v-if="!fields.length" class="text-sm text-muted">{{ t('pgNoConfig') }}</p>
          <footer class="flex flex-wrap justify-end gap-2 border-t border-default pt-4">
            <UButton
              type="button"
              color="neutral"
              variant="outline"
              :disabled="busy || loading || !dirty || (uncertain && !reconciled)"
              @click="ask('discard')"
              >{{ t('resetChanges') }}</UButton
            ><UButton type="submit" :loading="busy" :disabled="blocked || !dirty">{{
              t('pgSave')
            }}</UButton>
          </footer>
        </form>
        <section v-if="safePages.length" class="p-5 border-t border-default space-y-3">
          <h3 class="font-semibold text-sm">{{ t('pluginCustomPages') }}</h3>
          <div class="flex flex-wrap gap-2">
            <UButton
              v-for="page in safePages"
              :key="page.id"
              :href="page.entry"
              target="_blank"
              rel="noopener noreferrer"
              color="neutral"
              variant="outline"
              icon="i-lucide-external-link"
              >{{ page.title }}</UButton
            >
          </div>
        </section>
      </section>
      <section v-else class="rounded-lg border border-default bg-default p-8">
        <h2 class="font-medium">{{ t('pluginChoose') }}</h2>
        <p class="text-sm text-muted mt-2">{{ t('pgChooseHelp') }}</p>
      </section>
    </div>
    <UModal
      :open="confirmation"
      :title="confirmationCopy.title"
      :description="confirmationCopy.help"
      :dismissible="!busy"
      :ui="{ content: 'max-w-lg' }"
      @update:open="
        (open) => {
          if (!open) cancelConfirmation()
        }
      "
      ><template #footer
        ><div class="flex justify-end gap-2 w-full">
          <UButton color="neutral" variant="outline" :disabled="busy" @click="cancelConfirmation">{{
            t('cancel')
          }}</UButton
          ><UButton
            :color="confirmationKind === 'disable' ? 'error' : 'primary'"
            :loading="busy"
            :disabled="
              !confirmation ||
              busy ||
              (confirmationKind === 'discard' && uncertain && !reconciled && !pendingNavigation)
            "
            @click="confirm"
            >{{ confirmationCopy.action }}</UButton
          >
        </div></template
      ></UModal
    >
  </div>
</template>
