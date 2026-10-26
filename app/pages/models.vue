<script setup lang="ts">
import type { components, paths } from '~/types/api'
type Config = components['schemas']['ModelConfig']
const { t, locale } = useI18n(),
  { client } = useApi()
const config = ref<Config>({ providers: [], models: [], routes: [] }),
  error = ref(''),
  saved = ref(false),
  busy = ref(false)
type ProbeResponse =
  paths['/api/models/{id}/probe']['post']['responses'][200]['content']['application/json']
type Capability = ProbeResponse['capability']
const capabilities: Capability[] = ['text', 'tools', 'vision', 'embedding']
const lastSaved = ref(''),
  loaded = ref(false),
  discoveryBusy = ref(''),
  probeBusy = ref(false)
const pendingProbe = ref<{ id: string; capability: Capability }>()
const probeCapabilities = ref<Record<string, Capability>>({})
const probeResults = ref<Record<string, ProbeResponse>>({})
const usage = ref<components['schemas']['Usage'][]>([]),
  usageBusy = ref(false)
const dirty = computed(() => loaded.value && JSON.stringify(config.value) !== lastSaved.value)
const operationBusy = computed(() => busy.value || probeBusy.value || !!discoveryBusy.value)
function captureSaved() {
  for (const roleName of roles) route(roleName)
  lastSaved.value = JSON.stringify(config.value)
  loaded.value = true
}
function reset() {
  if (operationBusy.value || !lastSaved.value) return
  config.value = JSON.parse(lastSaved.value)
  pendingProbe.value = undefined
  error.value = ''
  saved.value = false
}
function price(
  model: Config['models'][number],
  field: 'inputUsdPerMillion' | 'outputUsdPerMillion',
  value: string | number,
) {
  if (value === '') delete model[field]
  else if (Number.isFinite(Number(value))) model[field] = Number(value)
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
async function runProbe() {
  if (operationBusy.value || dirty.value || !pendingProbe.value) return
  const request = { ...pendingProbe.value }
  pendingProbe.value = undefined
  probeBusy.value = true
  error.value = ''
  try {
    const result = await client.POST('/api/models/{id}/probe', {
      params: { path: { id: request.id } },
      body: { capability: request.capability },
    })
    if (result.data) probeResults.value[request.id] = result.data
    else error.value = result.error?.error ?? t('networkError')
    await loadUsage()
  } catch {
    error.value = t('networkError')
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
const protocols = ref<Array<{ id: string; label: string }>>([])
const protocolOptions = computed(() => [
  ...new Map(
    [
      ...protocols.value.map((p) => ({ label: p.label, value: p.id })),
      ...config.value.providers
        .filter((p) => !protocols.value.some((option) => option.id === p.protocol))
        .map((p) => ({ label: p.protocol + ' (' + t('unavailable') + ')', value: p.protocol })),
    ].map((p) => [p.value, p]),
  ).values(),
])
const roles = ['planner', 'thinker', 'learning', 'vision', 'embedding'] as const
async function load() {
  const [r, p] = await Promise.all([client.GET('/api/models'), client.GET('/api/model-protocols')])
  protocols.value = p.data ?? []
  if (r.data) {
    config.value = r.data
    captureSaved()
  } else error.value = r.error?.error ?? t('networkError')
}
onMounted(() => {
  void load().catch(() => (error.value = t('networkError')))
  void loadUsage()
})
async function save() {
  if (operationBusy.value || !loaded.value) return
  if (
    config.value.models.some((model) =>
      [model.inputUsdPerMillion, model.outputUsdPerMillion].some(
        (value) => value !== undefined && (!Number.isFinite(value) || value < 0),
      ),
    )
  ) {
    error.value = t('priceInvalid')
    return
  }
  pendingProbe.value = undefined
  busy.value = true
  saved.value = false
  error.value = ''
  try {
    const r = await client.PUT('/api/models', { body: config.value })
    if (r.data) {
      config.value = r.data
      captureSaved()
      probeResults.value = {}
      saved.value = true
    } else error.value = r.error?.error ?? t('networkError')
  } catch {
    error.value = t('networkError')
  } finally {
    busy.value = false
  }
}
function addProvider() {
  if (operationBusy.value || !loaded.value) return
  beginEdit('provider')
  config.value.providers.push({
    id: crypto.randomUUID(),
    name: 'OpenAI',
    protocol: 'openai-chat',
    baseUrl: 'https://api.openai.com/v1',
    credentialEnv: 'OPENAI_API_KEY',
  })
  editorId.value = config.value.providers.at(-1)!.id
}
function addModel() {
  if (operationBusy.value || !loaded.value || !config.value.providers.length) return
  beginEdit('model')
  config.value.models.push({
    id: crypto.randomUUID(),
    displayName: t('newModel'),
    providerId: config.value.providers[0]?.id ?? '',
    apiModelName: '',
    capabilities: ['text', 'tools'],
  })
  editorId.value = config.value.models.at(-1)!.id
}
function route(role: (typeof roles)[number]) {
  let r = config.value.routes.find((r) => r.role === role)
  if (!r) {
    r = { role, orderedModelIds: [], timeoutMs: 60000 }
    config.value.routes.push(r)
  }
  return r
}
function move(role: (typeof roles)[number], index: number, delta: number) {
  const ids = route(role).orderedModelIds
  const target = index + delta
  if (target < 0 || target >= ids.length) return
  ;[ids[index], ids[target]] = [ids[target]!, ids[index]!]
}
function removeModel(id: string) {
  config.value.models = config.value.models.filter((m) => m.id !== id)
  for (const r of config.value.routes) r.orderedModelIds = r.orderedModelIds.filter((i) => i !== id)
}
const suggestions = ref<Record<string, string[]>>({})
async function discover(id: string) {
  if (operationBusy.value || dirty.value || !loaded.value) return
  discoveryBusy.value = id
  error.value = ''
  try {
    const r = await client.GET('/api/providers/{id}/models', { params: { path: { id } } })
    if (r.data) suggestions.value[id] = r.data.models
    else error.value = r.error?.error ?? t('networkError')
  } catch {
    error.value = t('networkError')
  } finally {
    discoveryBusy.value = ''
  }
}

type Section = 'providers' | 'models' | 'routes' | 'usage'
const section = ref<Section>('models'),
  query = ref(''),
  providerFilter = ref('*')
const editorOpen = ref(false),
  editorKind = ref<'provider' | 'model' | 'route'>('model'),
  editorId = ref('')
let editorSnapshot = ''
const selectedProvider = computed(() => config.value.providers.find((p) => p.id === editorId.value))
const selectedModel = computed(() => config.value.models.find((m) => m.id === editorId.value))
const selectedRoute = computed(() => config.value.routes.find((r) => r.role === editorId.value))
const sections = computed(() => [
  { id: 'providers' as const, label: t('providers'), count: config.value.providers.length },
  { id: 'models' as const, label: t('modelInstances'), count: config.value.models.length },
  { id: 'routes' as const, label: t('roleRoutes'), count: roles.length },
  { id: 'usage' as const, label: t('usageTitle'), count: usage.value.length },
])
const matches = (...values: string[]) =>
  values.join(' ').toLocaleLowerCase().includes(query.value.toLocaleLowerCase().trim())
const filteredProviders = computed(() =>
  config.value.providers.filter((p) => matches(p.name, p.protocol, p.baseUrl)),
)
const filteredModels = computed(() =>
  config.value.models.filter(
    (m) =>
      (providerFilter.value === '*' || m.providerId === providerFilter.value) &&
      matches(m.displayName, m.apiModelName, providerName(m.providerId), ...m.capabilities),
  ),
)
const filteredRoles = computed(() =>
  roles.filter((roleName) => matches(roleName, ...route(roleName).orderedModelIds.map(modelName))),
)
const filteredUsage = computed(() =>
  usage.value.filter((row) => matches(row.day, row.role, row.apiModelName, modelName(row.modelId))),
)
const providerName = (id: string) => config.value.providers.find((p) => p.id === id)?.name ?? id
const modelName = (id: string) => config.value.models.find((m) => m.id === id)?.displayName ?? id
const editorTitle = computed(() =>
  editorKind.value === 'provider'
    ? selectedProvider.value?.name
    : editorKind.value === 'model'
      ? selectedModel.value?.displayName
      : selectedRoute.value?.role,
)
function beginEdit(kind: typeof editorKind.value, id = '') {
  if (operationBusy.value || !loaded.value) return
  editorSnapshot = JSON.stringify(config.value)
  editorKind.value = kind
  editorId.value = id
  editorOpen.value = true
  saved.value = false
}
async function saveEditor() {
  await save()
  if (saved.value && !dirty.value) editorOpen.value = false
}
function cancelEdit() {
  if (operationBusy.value) return
  config.value = JSON.parse(editorSnapshot)
  editorOpen.value = false
}
function removeSelected() {
  if (operationBusy.value) return
  if (editorKind.value === 'model') removeModel(editorId.value)
  else if (
    editorKind.value === 'provider' &&
    !config.value.models.some((m) => m.providerId === editorId.value)
  )
    config.value.providers = config.value.providers.filter((p) => p.id !== editorId.value)
  editorOpen.value = false
}
watch(editorOpen, (open) => {
  if (!open) pendingProbe.value = undefined
})
watch(section, () => {
  query.value = ''
})
</script>
<template>
  <div class="space-y-4">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h1 class="page-heading">{{ t('models') }}</h1>
        <p class="text-sm text-muted mt-1">{{ t('modelsHelp') }}</p>
      </div>
      <div class="flex items-center gap-2">
        <UBadge v-if="dirty" color="warning" variant="subtle">{{ t('uxUnsaved') }}</UBadge>
        <UButton variant="ghost" :disabled="operationBusy || !dirty" @click="reset">{{
          t('reset')
        }}</UButton>
        <UButton
          icon="i-lucide-save"
          :loading="busy"
          :disabled="operationBusy || !loaded || !dirty"
          @click="save"
          >{{ t('save') }}</UButton
        >
      </div>
    </header>
    <UAlert v-if="error && !editorOpen" color="error" :title="error" />
    <UAlert v-if="saved && !dirty" color="success" :title="t('savedNextTurn')" />
    <div class="rounded-lg border border-default bg-default overflow-hidden">
      <nav
        class="flex gap-1 overflow-x-auto border-b border-default px-3 pt-2"
        :aria-label="t('models')"
      >
        <UButton
          v-for="item in sections"
          :key="item.id"
          :variant="section === item.id ? 'soft' : 'ghost'"
          :color="section === item.id ? 'primary' : 'neutral'"
          class="shrink-0 rounded-b-none"
          :aria-current="section === item.id ? 'page' : undefined"
          @click="section = item.id"
        >
          {{ item.label }}
          <UBadge size="sm" variant="subtle" color="neutral">{{ item.count }}</UBadge>
        </UButton>
      </nav>
      <div class="flex flex-wrap items-center gap-2 px-4 py-3 border-b border-default">
        <UInput
          v-model="query"
          icon="i-lucide-search"
          :placeholder="t('uxSearchModels')"
          :aria-label="t('uxSearchModels')"
          class="w-full sm:w-72"
        />
        <USelect
          v-if="section === 'models'"
          v-model="providerFilter"
          :aria-label="t('provider')"
          :items="[
            { label: t('uxAllProviders'), value: '*' },
            ...config.providers.map((p) => ({ label: p.name, value: p.id })),
          ]"
          class="w-48"
        />
        <div class="flex-1" />
        <UButton
          v-if="section === 'providers' || section === 'models'"
          icon="i-lucide-plus"
          :disabled="operationBusy || !loaded || (section === 'models' && !config.providers.length)"
          @click="section === 'providers' ? addProvider() : addModel()"
          >{{ t('add') }}</UButton
        >
        <UButton
          v-if="section === 'usage'"
          icon="i-lucide-refresh-cw"
          variant="ghost"
          :loading="usageBusy"
          :disabled="usageBusy"
          @click="loadUsage"
          >{{ t('refresh') }}</UButton
        >
      </div>
      <div class="overflow-x-auto">
        <table v-if="section === 'providers'" class="w-full text-left text-sm">
          <thead class="bg-elevated/50 text-muted text-xs">
            <tr>
              <th class="px-4 h-10">{{ t('name') }}</th>
              <th class="px-4">{{ t('protocol') }}</th>
              <th class="px-4">{{ t('baseUrl') }}</th>
              <th class="px-4">{{ t('modelInstances') }}</th>
              <th class="px-4">
                <span class="sr-only">{{ t('edit') }}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="provider in filteredProviders"
              :key="provider.id"
              class="border-t border-default hover:bg-elevated/40"
            >
              <td class="px-4 h-12 min-w-48 font-medium">
                <button
                  class="text-left hover:text-primary break-words"
                  :disabled="operationBusy"
                  @click="beginEdit('provider', provider.id)"
                >
                  {{ provider.name }}
                </button>
              </td>
              <td class="px-4 whitespace-nowrap">
                {{ provider.protocol
                }}<UBadge
                  v-if="!protocols.some((p) => p.id === provider.protocol)"
                  color="warning"
                  variant="subtle"
                  class="ml-2"
                  >{{ t('unavailable') }}</UBadge
                >
              </td>
              <td class="px-4 text-muted max-w-80 truncate" :title="provider.baseUrl">
                {{ provider.baseUrl }}
              </td>
              <td class="px-4">
                {{ config.models.filter((m) => m.providerId === provider.id).length }}
              </td>
              <td class="px-4 text-right">
                <UButton
                  icon="i-lucide-pencil"
                  variant="ghost"
                  color="neutral"
                  :aria-label="t('edit') + ': ' + provider.name"
                  :disabled="operationBusy"
                  @click="beginEdit('provider', provider.id)"
                />
              </td>
            </tr>
            <tr v-if="!filteredProviders.length">
              <td colspan="5" class="p-8 text-center text-muted">
                {{ t(config.providers.length ? 'uxNoMatches' : 'emptyProviders') }}
              </td>
            </tr>
          </tbody>
        </table>
        <table v-else-if="section === 'models'" class="w-full text-left text-sm">
          <thead class="bg-elevated/50 text-muted text-xs">
            <tr>
              <th class="px-4 h-10">{{ t('name') }}</th>
              <th class="px-4">{{ t('provider') }}</th>
              <th class="px-4">{{ t('apiModel') }}</th>
              <th class="px-4">{{ t('capabilities') }}</th>
              <th class="px-4">
                <span class="sr-only">{{ t('edit') }}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="model in filteredModels"
              :key="model.id"
              class="border-t border-default hover:bg-elevated/40"
            >
              <td class="px-4 h-12 min-w-56 max-w-80 font-medium">
                <button
                  class="text-left break-words hover:text-primary"
                  :disabled="operationBusy"
                  @click="beginEdit('model', model.id)"
                >
                  {{ model.displayName }}
                </button>
              </td>
              <td class="px-4 max-w-48 truncate" :title="providerName(model.providerId)">
                {{ providerName(model.providerId) }}
              </td>
              <td
                class="px-4 min-w-40 max-w-72 truncate font-mono text-xs text-muted"
                :title="model.apiModelName"
              >
                {{ model.apiModelName || '—' }}
              </td>
              <td class="px-4">
                <div class="flex gap-1 flex-wrap py-1">
                  <UBadge
                    v-for="cap in model.capabilities"
                    :key="cap"
                    color="neutral"
                    variant="subtle"
                    size="sm"
                    >{{ cap }}</UBadge
                  >
                </div>
              </td>
              <td class="px-4 text-right">
                <UButton
                  icon="i-lucide-pencil"
                  variant="ghost"
                  color="neutral"
                  :aria-label="t('edit') + ': ' + model.displayName"
                  :disabled="operationBusy"
                  @click="beginEdit('model', model.id)"
                />
              </td>
            </tr>
            <tr v-if="!filteredModels.length">
              <td colspan="5" class="p-8 text-center text-muted">{{ t('uxNoMatches') }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else-if="section === 'routes'">
          <p class="px-4 py-3 text-sm text-muted border-b border-default">{{ t('routeHelp') }}</p>
          <button
            v-for="roleName in filteredRoles"
            :key="roleName"
            class="w-full flex items-center gap-4 px-4 min-h-14 text-left border-b border-default last:border-0 hover:bg-elevated/40"
            :disabled="operationBusy || !loaded"
            @click="beginEdit('route', roleName)"
          >
            <span class="w-24 shrink-0 font-medium capitalize text-sm">{{ roleName }}</span
            ><span class="flex-1 flex gap-1 flex-wrap py-2"
              ><UBadge
                v-for="(id, index) in route(roleName).orderedModelIds"
                :key="id"
                color="neutral"
                variant="subtle"
                >{{ index + 1 }} · {{ modelName(id) }}</UBadge
              ><span v-if="!route(roleName).orderedModelIds.length" class="text-sm text-muted">{{
                t('uxUnassigned')
              }}</span></span
            ><span class="text-xs text-muted whitespace-nowrap"
              >{{ route(roleName).timeoutMs }} ms</span
            ><UIcon name="i-lucide-chevron-right" class="size-4 text-muted" />
          </button>
        </div>
        <template v-else>
          <p class="px-4 py-3 text-sm text-muted">{{ t('usageEstimateHelp') }}</p>
          <table class="w-full text-sm text-left">
            <thead class="bg-elevated/50 text-muted text-xs">
              <tr>
                <th class="px-4 h-10">{{ t('usageDay') }}</th>
                <th class="px-4">{{ t('modelInstances') }}</th>
                <th class="px-4">{{ t('usageRole') }}</th>
                <th class="px-4">{{ t('usageCalls') }}</th>
                <th class="px-4">{{ t('usageTokens') }}</th>
                <th class="px-4">{{ t('usageDuration') }}</th>
                <th class="px-4">{{ t('usageCost') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="row in filteredUsage"
                :key="
                  JSON.stringify([row.day, row.modelId, row.providerId, row.apiModelName, row.role])
                "
                class="border-t border-default"
              >
                <td class="px-4 h-12 whitespace-nowrap">{{ row.day }}</td>
                <td class="px-4 min-w-48">
                  <div>{{ modelName(row.modelId) }}</div>
                  <div class="text-xs text-muted">
                    {{ row.providerId }} · {{ row.apiModelName }}
                  </div>
                </td>
                <td class="px-4">{{ row.role }}</td>
                <td class="px-4">{{ row.calls }}</td>
                <td class="px-4 whitespace-nowrap">
                  {{ row.inputTokens }} / {{ row.outputTokens }}
                </td>
                <td class="px-4 whitespace-nowrap">{{ row.durationMs }} ms</td>
                <td class="px-4 whitespace-nowrap">
                  <div>{{ cost(row) }}</div>
                  <div class="text-xs text-muted">
                    {{ t('usagePriced', { priced: row.pricedCalls, total: row.calls }) }}
                  </div>
                </td>
              </tr>
              <tr v-if="!filteredUsage.length">
                <td colspan="7" class="p-8 text-center text-muted">
                  {{ t(usage.length ? 'uxNoMatches' : 'usageEmpty') }}
                </td>
              </tr>
            </tbody>
          </table>
        </template>
      </div>
      <p v-if="dirty" class="px-4 py-2 border-t border-default text-xs text-warning">
        {{ t('uxDraftHelp') }}
      </p>
    </div>
    <USlideover
      v-model:open="editorOpen"
      :title="editorTitle || t('edit')"
      :description="t('uxEditorDraftHelp')"
      :dismissible="!operationBusy"
      :close="!operationBusy"
      :ui="{ content: 'w-full sm:max-w-[620px]', body: 'space-y-5', footer: 'justify-between' }"
    >
      <template #body>
        <UAlert v-if="error" color="error" :title="error" />
        <fieldset :disabled="operationBusy" class="space-y-5 min-w-0">
          <template v-if="editorKind === 'provider' && selectedProvider">
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <UFormField :label="t('name')"
                ><UInput v-model="selectedProvider.name" class="w-full"
              /></UFormField>
              <UFormField :label="t('protocol')"
                ><USelect
                  v-model="selectedProvider.protocol"
                  :items="protocolOptions"
                  class="w-full"
              /></UFormField>
              <UFormField :label="t('baseUrl')" class="sm:col-span-2"
                ><UInput v-model="selectedProvider.baseUrl" class="w-full"
              /></UFormField>
              <UFormField
                :label="t('credentialEnv')"
                :help="t('credentialHelp')"
                class="sm:col-span-2"
                ><UInput v-model="selectedProvider.credentialEnv" class="w-full"
              /></UFormField>
            </div>
            <p class="text-xs text-muted break-all">ID: {{ selectedProvider.id }}</p>
            <div class="rounded-lg border border-default p-4 space-y-3">
              <p class="text-sm text-muted">{{ t('probeSaveFirst') }}</p>
              <UButton
                variant="soft"
                icon="i-lucide-list-restart"
                :disabled="dirty"
                :loading="discoveryBusy === selectedProvider.id"
                @click="discover(selectedProvider.id)"
                >{{ t('discoverModels') }}</UButton
              >
              <div
                v-if="suggestions[selectedProvider.id]"
                class="max-h-48 overflow-y-auto text-xs font-mono space-y-1"
              >
                <p v-for="name in suggestions[selectedProvider.id]" :key="name">{{ name }}</p>
              </div>
            </div>
            <p
              v-if="config.models.some((m) => m.providerId === selectedProvider?.id)"
              class="text-sm text-muted"
            >
              {{ t('uxProviderInUse') }}
            </p>
          </template>
          <template v-if="editorKind === 'model' && selectedModel">
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <UFormField :label="t('name')"
                ><UInput v-model="selectedModel.displayName" class="w-full"
              /></UFormField>
              <UFormField :label="t('provider')"
                ><USelect
                  v-model="selectedModel.providerId"
                  :items="config.providers.map((p) => ({ label: p.name, value: p.id }))"
                  class="w-full"
              /></UFormField>
              <UFormField :label="t('apiModel')" class="sm:col-span-2"
                ><UInput
                  v-model="selectedModel.apiModelName"
                  :list="selectedModel.id"
                  class="w-full" /><datalist :id="selectedModel.id">
                  <option
                    v-for="name in suggestions[selectedModel.providerId] ?? []"
                    :key="name"
                    :value="name"
                  /></datalist
              ></UFormField>
              <UFormField :label="t('capabilities')" class="sm:col-span-2"
                ><USelect
                  v-model="selectedModel.capabilities"
                  multiple
                  :items="capabilities"
                  class="w-full"
              /></UFormField>
            </div>
            <p class="text-xs text-muted break-all">ID: {{ selectedModel.id }}</p>
            <details class="rounded-lg border border-default p-4">
              <summary class="cursor-pointer text-sm font-medium">
                {{ t('uxAdvancedModels') }}
              </summary>
              <div class="space-y-5 pt-4">
                <div class="grid sm:grid-cols-2 gap-4">
                  <UFormField :label="t('inputPrice')" :help="t('priceOptionalHelp')"
                    ><UInput
                      type="number"
                      min="0"
                      step="any"
                      class="w-full"
                      :model-value="String(selectedModel.inputUsdPerMillion ?? '')"
                      @update:model-value="
                        (value) =>
                          selectedModel && price(selectedModel, 'inputUsdPerMillion', value)
                      " /></UFormField
                  ><UFormField :label="t('outputPrice')" :help="t('priceOptionalHelp')"
                    ><UInput
                      type="number"
                      min="0"
                      step="any"
                      class="w-full"
                      :model-value="String(selectedModel.outputUsdPerMillion ?? '')"
                      @update:model-value="
                        (value) =>
                          selectedModel && price(selectedModel, 'outputUsdPerMillion', value)
                      "
                  /></UFormField>
                </div>
                <p class="text-sm text-muted">{{ t('probeHelp') }}</p>
                <p v-if="dirty" class="text-xs text-warning">{{ t('probeSaveFirst') }}</p>
                <div class="flex gap-2">
                  <USelect
                    :model-value="probeCapabilities[selectedModel.id] ?? 'text'"
                    :items="capabilities"
                    :aria-label="t('capabilities')"
                    @update:model-value="
                      (value) => selectedModel && (probeCapabilities[selectedModel.id] = value)
                    "
                  /><UButton
                    variant="soft"
                    :disabled="dirty"
                    @click="
                      pendingProbe = {
                        id: selectedModel.id,
                        capability: probeCapabilities[selectedModel.id] ?? 'text',
                      }
                    "
                    >{{ t('probeCapability') }}</UButton
                  >
                </div>
                <div v-if="pendingProbe" class="rounded-md border border-warning p-3 space-y-3">
                  <p class="text-sm">{{ t('probeChargeWarning') }}</p>
                  <p class="text-xs text-muted">
                    {{ selectedModel.displayName }} · {{ pendingProbe.capability }}
                  </p>
                  <div class="flex gap-2">
                    <UButton :disabled="operationBusy || dirty" @click="runProbe">{{
                      t('probeConfirm')
                    }}</UButton
                    ><UButton variant="ghost" @click="pendingProbe = undefined">{{
                      t('cancel')
                    }}</UButton>
                  </div>
                </div>
                <UAlert
                  v-if="probeResults[selectedModel.id] && !dirty"
                  :color="probeResults[selectedModel.id]?.ok ? 'success' : 'warning'"
                  :title="t(probeResults[selectedModel.id]?.ok ? 'probePassed' : 'probeFailed')"
                  :description="`${probeResults[selectedModel.id]?.capability}: ${probeResults[selectedModel.id]?.detail} (${probeResults[selectedModel.id]?.durationMs} ms)`"
                />
              </div>
            </details>
          </template>
          <template v-if="editorKind === 'route' && selectedRoute">
            <p class="text-sm text-muted">{{ t('routeHelp') }}</p>
            <div
              v-for="(id, index) in selectedRoute.orderedModelIds"
              :key="id"
              class="flex items-center gap-2 rounded-md border border-default px-3 py-2"
            >
              <UBadge color="neutral" variant="subtle">{{ index + 1 }}</UBadge
              ><span class="flex-1 text-sm break-words">{{ modelName(id) }}</span
              ><UButton
                icon="i-lucide-arrow-up"
                variant="ghost"
                :aria-label="t('moveUp')"
                :disabled="index === 0"
                @click="move(selectedRoute.role, index, -1)"
              /><UButton
                icon="i-lucide-arrow-down"
                variant="ghost"
                :aria-label="t('moveDown')"
                :disabled="index === selectedRoute.orderedModelIds.length - 1"
                @click="move(selectedRoute.role, index, 1)"
              /><UButton
                icon="i-lucide-x"
                variant="ghost"
                :aria-label="t('remove')"
                @click="selectedRoute.orderedModelIds.splice(index, 1)"
              />
            </div>
            <USelect
              :placeholder="t('addModel')"
              :aria-label="t('addModel')"
              :model-value="undefined"
              :items="
                config.models
                  .filter((m) => !selectedRoute?.orderedModelIds.includes(m.id))
                  .map((m) => ({ label: m.displayName, value: m.id }))
              "
              class="w-full"
              @update:model-value="
                (value) => {
                  if (value && selectedRoute) selectedRoute.orderedModelIds.push(value)
                }
              "
            />
            <UFormField :label="t('uxRouteTimeout')"
              ><UInput
                v-model.number="selectedRoute.timeoutMs"
                type="number"
                min="100"
                max="300000"
                step="1"
                class="w-full"
            /></UFormField>
          </template>
        </fieldset>
      </template>
      <template #footer
        ><UButton
          v-if="editorKind !== 'route'"
          color="error"
          variant="ghost"
          :disabled="
            operationBusy ||
            (editorKind === 'provider' && config.models.some((m) => m.providerId === editorId))
          "
          @click="removeSelected"
          >{{ t('remove') }}</UButton
        ><span v-else />
        <div class="flex gap-2">
          <UButton color="neutral" variant="ghost" :disabled="operationBusy" @click="cancelEdit">{{
            t('cancel')
          }}</UButton
          ><UButton :loading="busy" :disabled="operationBusy || !dirty" @click="saveEditor">{{
            t('save')
          }}</UButton>
        </div></template
      >
    </USlideover>
  </div>
</template>
