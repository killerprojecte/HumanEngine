<script setup lang="ts">
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
const embeddingPanel = ref<{ refresh: () => Promise<void> }>()
const roles = ['planner', 'thinker', 'learning', 'vision', 'embedding'] as const
const capabilities: Capability[] = ['text', 'tools', 'vision', 'embedding']
const capabilityKeys: Record<Capability, string> = {
  text: 'mcCapText',
  tools: 'mcCapTools',
  vision: 'mcCapVision',
  embedding: 'mcCapEmbedding',
}
const roleKeys: Record<Route['role'], string> = {
  planner: 'mcRolePlanner',
  thinker: 'mcRoleThinker',
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
    timeoutMs: 60000,
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
      roles.includes(row.role as Route['role']) ? roleLabel(row.role as Route['role']) : row.role,
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
async function loadUsage() {
  if (usageBusy.value) return
  usageBusy.value = true
  try {
    const result = await client.GET('/api/usage')
    if (result.data) usage.value = result.data
    else error.value = result.error?.error ?? t('networkError')
  } catch {
    error.value = t('networkError')
  } finally {
    usageBusy.value = false
  }
}
onMounted(() => {
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
  if (!deleting && modelDraft.value && !modelDraft.value.capabilities.length) {
    editorError.value = t('mcRequiredCapability')
    return
  }
  if (
    !deleting &&
    modelDraft.value &&
    [modelDraft.value.inputUsdPerMillion, modelDraft.value.outputUsdPerMillion].some(
      (value) => value !== undefined && (!Number.isFinite(value) || value < 0),
    )
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
        timeoutMs: 60000,
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
function setProviderField(
  field: 'name' | 'protocol' | 'baseUrl' | 'credentialEnv',
  value: string | number,
) {
  if (providerDraft.value) providerDraft.value[field] = String(value)
}
function price(field: 'inputUsdPerMillion' | 'outputUsdPerMillion', value: string | number) {
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
function cost(row: components['schemas']['Usage']) {
  return row.pricedCalls
    ? new Intl.NumberFormat(locale.value, {
        style: 'currency',
        currency: 'USD',
        maximumFractionDigits: 6,
      }).format(row.estimatedUsd)
    : t('usageUnknownCost')
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
        <p class="p-4 text-sm text-muted">{{ t('usageEstimateHelp') }}</p>
        <div class="overflow-x-auto">
          <table class="model-table w-full text-left text-sm">
            <thead>
              <tr>
                <th>{{ t('usageDay') }}</th>
                <th>{{ t('mcModelName') }}</th>
                <th>{{ t('mcTask') }}</th>
                <th>{{ t('usageCalls') }}</th>
                <th>{{ t('usageTokens') }}</th>
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
                <td class="whitespace-nowrap">{{ row.day }}</td>
                <td class="min-w-52">
                  <div>{{ modelName(row.modelId) }}</div>
                  <p class="text-xs text-muted mt-1">
                    {{ providerName(row.providerId) }} · {{ row.apiModelName }}
                  </p>
                </td>
                <td>
                  {{
                    roles.includes(row.role as Route['role'])
                      ? roleLabel(row.role as Route['role'])
                      : row.role
                  }}
                </td>
                <td>{{ row.calls }}</td>
                <td class="whitespace-nowrap">{{ row.inputTokens }} / {{ row.outputTokens }}</td>
                <td class="whitespace-nowrap">{{ row.durationMs }} ms</td>
                <td class="whitespace-nowrap">
                  <div>{{ cost(row) }}</div>
                  <p class="text-xs text-muted mt-1">
                    {{ t('usagePriced', { priced: row.pricedCalls, total: row.calls }) }}
                  </p>
                </td>
              </tr>
              <tr v-if="!filteredUsage.length">
                <td colspan="7" class="p-8 text-center text-muted">
                  {{ t(usage.length ? 'uxNoMatches' : 'usageEmpty') }}
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
                <div class="grid sm:grid-cols-2 gap-4 mt-4">
                  <UFormField :label="t('inputPrice')"
                    ><UInput
                      type="number"
                      min="0"
                      step="any"
                      class="w-full"
                      :model-value="String(selectedModel.inputUsdPerMillion ?? '')"
                      @update:model-value="
                        (value) => price('inputUsdPerMillion', value)
                      " /></UFormField
                  ><UFormField :label="t('outputPrice')"
                    ><UInput
                      type="number"
                      min="0"
                      step="any"
                      class="w-full"
                      :model-value="String(selectedModel.outputUsdPerMillion ?? '')"
                      @update:model-value="(value) => price('outputUsdPerMillion', value)"
                  /></UFormField>
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
                <UFormField :label="t('mcTimeout')" class="mt-4"
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
