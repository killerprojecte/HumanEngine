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
  config.value.providers.push({
    id: crypto.randomUUID(),
    name: 'OpenAI',
    protocol: 'openai-chat',
    baseUrl: 'https://api.openai.com/v1',
    credentialEnv: 'OPENAI_API_KEY',
  })
}
function addModel() {
  config.value.models.push({
    id: crypto.randomUUID(),
    displayName: t('newModel'),
    providerId: config.value.providers[0]?.id ?? '',
    apiModelName: '',
    capabilities: ['text', 'tools'],
  })
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
</script>
<template>
  <div class="space-y-6">
    <div class="flex justify-between">
      <div>
        <h1 class="text-2xl font-semibold">{{ t('models') }}</h1>
        <p class="text-muted">{{ t('modelsHelp') }}</p>
      </div>
      <div class="flex gap-2">
        <UButton variant="ghost" :disabled="operationBusy || !dirty" @click="reset">{{
          t('reset')
        }}</UButton
        ><UButton
          :loading="busy"
          :disabled="operationBusy || !loaded"
          icon="i-lucide-save"
          @click="save"
          >{{ t('save') }}</UButton
        >
      </div>
    </div>
    <UAlert v-if="error" color="error" :title="error" /><UAlert
      v-if="saved && !dirty"
      color="success"
      :title="t('savedNextTurn')"
    />
    <UAlert v-if="dirty" color="warning" :title="t('probeSaveFirst')" />
    <UCard v-if="pendingProbe"
      ><p class="font-medium">{{ t('probeChargeWarning') }}</p>
      <p class="text-sm text-muted">
        {{ config.models.find((m) => m.id === pendingProbe?.id)?.displayName }} ·
        {{ pendingProbe.capability }}
      </p>
      <div class="flex gap-2 mt-3">
        <UButton :disabled="operationBusy || dirty" @click="runProbe">{{
          t('probeConfirm')
        }}</UButton
        ><UButton variant="ghost" @click="pendingProbe = undefined">{{ t('cancel') }}</UButton>
      </div></UCard
    >
    <fieldset :disabled="operationBusy || !loaded" class="space-y-6 min-w-0">
      <UCard
        ><template #header
          ><div class="flex justify-between">
            <h2 class="font-semibold">{{ t('providers') }}</h2>
            <UButton size="sm" variant="soft" icon="i-lucide-plus" @click="addProvider">{{
              t('add')
            }}</UButton>
          </div></template
        >
        <div v-if="!config.providers.length" class="text-muted">{{ t('emptyProviders') }}</div>
        <div
          v-for="provider in config.providers"
          :key="provider.id"
          class="grid md:grid-cols-2 gap-4 py-5 border-b border-default last:border-0"
        >
          <UFormField :label="t('name')"
            ><UInput v-model="provider.name" class="w-full" /></UFormField
          ><UFormField :label="t('protocol')"
            ><USelect
              v-model="provider.protocol"
              :items="protocolOptions"
              class="w-full" /></UFormField
          ><UFormField :label="t('baseUrl')"
            ><UInput v-model="provider.baseUrl" class="w-full" /></UFormField
          ><UFormField :label="t('credentialEnv')" :help="t('credentialHelp')"
            ><UInput v-model="provider.credentialEnv" class="w-full"
          /></UFormField>
          <div class="flex gap-2">
            <UButton
              size="sm"
              variant="soft"
              :loading="discoveryBusy === provider.id"
              :disabled="dirty"
              @click="discover(provider.id)"
              >{{ t('discoverModels') }}</UButton
            ><UButton
              size="sm"
              color="error"
              variant="ghost"
              :disabled="config.models.some((m) => m.providerId === provider.id)"
              @click="config.providers = config.providers.filter((p) => p.id !== provider.id)"
              >{{ t('remove') }}</UButton
            >
          </div>
        </div></UCard
      >
      <UCard
        ><template #header
          ><div class="flex justify-between">
            <h2 class="font-semibold">{{ t('modelInstances') }}</h2>
            <UButton
              size="sm"
              variant="soft"
              icon="i-lucide-plus"
              :disabled="!config.providers.length"
              @click="addModel"
              >{{ t('add') }}</UButton
            >
          </div></template
        >
        <div
          v-for="model in config.models"
          :key="model.id"
          class="grid md:grid-cols-2 gap-4 py-5 border-b border-default last:border-0"
        >
          <UFormField :label="t('name')"
            ><UInput v-model="model.displayName" class="w-full" /></UFormField
          ><UFormField :label="t('provider')"
            ><USelect
              v-model="model.providerId"
              :items="config.providers.map((p) => ({ label: p.name, value: p.id }))"
              class="w-full" /></UFormField
          ><UFormField :label="t('apiModel')"
            ><UInput v-model="model.apiModelName" :list="model.id" class="w-full" /><datalist
              :id="model.id"
            >
              <option
                v-for="name in suggestions[model.providerId] ?? []"
                :key="name"
                :value="name"
              /></datalist></UFormField
          ><UFormField :label="t('capabilities')"
            ><USelect
              v-model="model.capabilities"
              multiple
              :items="['text', 'tools', 'vision', 'embedding']"
              class="w-full"
          /></UFormField>
          <UFormField :label="t('inputPrice')" :help="t('priceOptionalHelp')"
            ><UInput
              type="number"
              min="0"
              step="any"
              :model-value="String(model.inputUsdPerMillion ?? '')"
              @update:model-value="(value) => price(model, 'inputUsdPerMillion', value)"
          /></UFormField>
          <UFormField :label="t('outputPrice')" :help="t('priceOptionalHelp')"
            ><UInput
              type="number"
              min="0"
              step="any"
              :model-value="String(model.outputUsdPerMillion ?? '')"
              @update:model-value="(value) => price(model, 'outputUsdPerMillion', value)"
          /></UFormField>
          <div class="md:col-span-2 space-y-2">
            <p class="text-sm text-muted">{{ t('probeHelp') }}</p>
            <div class="flex gap-2">
              <USelect
                :model-value="probeCapabilities[model.id] ?? 'text'"
                :items="capabilities"
                @update:model-value="(value) => (probeCapabilities[model.id] = value)"
              /><UButton
                variant="soft"
                :disabled="dirty"
                @click="
                  pendingProbe = { id: model.id, capability: probeCapabilities[model.id] ?? 'text' }
                "
                >{{ t('probeCapability') }}</UButton
              >
            </div>
            <UAlert
              v-if="probeResults[model.id] && !dirty"
              :color="probeResults[model.id]?.ok ? 'success' : 'warning'"
              :title="t(probeResults[model.id]?.ok ? 'probePassed' : 'probeFailed')"
              :description="`${probeResults[model.id]?.capability}: ${probeResults[model.id]?.detail} (${probeResults[model.id]?.durationMs} ms)`"
            />
          </div>
          <div class="text-xs text-muted break-all">ID: {{ model.id }}</div>
          <UButton
            size="sm"
            variant="ghost"
            color="error"
            class="justify-self-end"
            @click="removeModel(model.id)"
            >{{ t('remove') }}</UButton
          >
        </div></UCard
      >
      <UCard
        ><template #header
          ><h2 class="font-semibold">{{ t('roleRoutes') }}</h2></template
        >
        <p class="text-muted text-sm mb-5">{{ t('routeHelp') }}</p>
        <div v-for="role in roles" :key="role" class="py-4 border-b border-default last:border-0">
          <h3 class="font-medium capitalize mb-3">{{ role }}</h3>
          <div
            v-for="(id, index) in route(role).orderedModelIds"
            :key="id"
            class="flex gap-2 items-center mb-2"
          >
            <UBadge variant="soft">{{ index + 1 }}</UBadge
            ><span class="flex-1">{{
              config.models.find((m) => m.id === id)?.displayName ?? id
            }}</span
            ><UButton
              :aria-label="t('moveUp')"
              icon="i-lucide-arrow-up"
              variant="ghost"
              :disabled="index === 0"
              @click="move(role, index, -1)"
            /><UButton
              :aria-label="t('moveDown')"
              icon="i-lucide-arrow-down"
              variant="ghost"
              :disabled="index === route(role).orderedModelIds.length - 1"
              @click="move(role, index, 1)"
            /><UButton
              :aria-label="t('remove')"
              icon="i-lucide-x"
              variant="ghost"
              @click="route(role).orderedModelIds.splice(index, 1)"
            />
          </div>
          <USelect
            :placeholder="t('addModel')"
            :items="
              config.models
                .filter((m) => !route(role).orderedModelIds.includes(m.id))
                .map((m) => ({ label: m.displayName, value: m.id }))
            "
            class="w-full max-w-sm"
            @update:model-value="
              (value) => {
                if (value) route(role).orderedModelIds.push(value)
              }
            "
          /></div
      ></UCard>
    </fieldset>
    <UCard
      ><template #header
        ><div class="flex justify-between">
          <h2 class="font-semibold">{{ t('usageTitle') }}</h2>
          <UButton variant="ghost" :loading="usageBusy" :disabled="usageBusy" @click="loadUsage">{{
            t('refresh')
          }}</UButton>
        </div></template
      >
      <p class="text-sm text-muted mb-4">{{ t('usageEstimateHelp') }}</p>
      <p v-if="!usage.length" class="text-muted">{{ t('usageEmpty') }}</p>
      <div v-else class="overflow-x-auto">
        <table class="w-full text-sm text-left">
          <thead>
            <tr>
              <th class="p-2">{{ t('usageDay') }}</th>
              <th class="p-2">{{ t('modelInstances') }}</th>
              <th class="p-2">{{ t('usageRole') }}</th>
              <th class="p-2">{{ t('usageCalls') }}</th>
              <th class="p-2">{{ t('usageTokens') }}</th>
              <th class="p-2">{{ t('usageDuration') }}</th>
              <th class="p-2">{{ t('usageCost') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="row in usage"
              :key="
                JSON.stringify([row.day, row.modelId, row.providerId, row.apiModelName, row.role])
              "
              class="border-t border-default"
            >
              <td class="p-2 whitespace-nowrap">{{ row.day }}</td>
              <td class="p-2">
                <div>
                  {{ config.models.find((m) => m.id === row.modelId)?.displayName ?? row.modelId }}
                </div>
                <div class="text-xs text-muted">{{ row.providerId }} · {{ row.apiModelName }}</div>
              </td>
              <td class="p-2">{{ row.role }}</td>
              <td class="p-2">{{ row.calls }}</td>
              <td class="p-2">{{ row.inputTokens }} / {{ row.outputTokens }}</td>
              <td class="p-2">{{ row.durationMs }} ms</td>
              <td class="p-2">
                <div>{{ cost(row) }}</div>
                <div class="text-xs text-muted">
                  {{ t('usagePriced', { priced: row.pricedCalls, total: row.calls }) }}
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </UCard>
  </div>
</template>
