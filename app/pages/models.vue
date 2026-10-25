<script setup lang="ts">
import type { components } from '~/types/api'
type Config = components['schemas']['ModelConfig']
const { t } = useI18n(),
  { client } = useApi()
const config = ref<Config>({ providers: [], models: [], routes: [] }),
  error = ref(''),
  saved = ref(false),
  busy = ref(false)
const roles = ['planner', 'thinker', 'learning', 'vision', 'embedding'] as const
async function load() {
  const r = await client.GET('/api/models')
  if (r.data) config.value = r.data
  else error.value = r.error?.error ?? t('networkError')
}
onMounted(() => load().catch(() => (error.value = t('networkError'))))
async function save() {
  busy.value = true
  saved.value = false
  error.value = ''
  try {
    const r = await client.PUT('/api/models', { body: config.value })
    if (r.data) {
      config.value = r.data
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
    displayName: 'New model',
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
  error.value = ''
  try {
    const r = await client.GET('/api/providers/{id}/models', { params: { path: { id } } })
    if (r.data) suggestions.value[id] = r.data.models
    else error.value = r.error?.error ?? t('networkError')
  } catch {
    error.value = t('networkError')
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
      <UButton :loading="busy" icon="i-lucide-save" @click="save">{{ t('save') }}</UButton>
    </div>
    <UAlert v-if="error" color="error" :title="error" /><UAlert
      v-if="saved"
      color="success"
      :title="t('savedNextTurn')"
    />
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
        <UFormField :label="t('name')"><UInput v-model="provider.name" class="w-full" /></UFormField
        ><UFormField :label="t('protocol')"
          ><USelect
            v-model="provider.protocol"
            :items="[
              { label: 'OpenAI Chat Completions', value: 'openai-chat' },
              { label: 'OpenAI Responses', value: 'openai-responses' },
            ]"
            class="w-full" /></UFormField
        ><UFormField :label="t('baseUrl')"
          ><UInput v-model="provider.baseUrl" class="w-full" /></UFormField
        ><UFormField :label="t('credentialEnv')" :help="t('credentialHelp')"
          ><UInput v-model="provider.credentialEnv" class="w-full"
        /></UFormField>
        <div class="flex gap-2">
          <UButton size="sm" variant="soft" @click="discover(provider.id)">{{
            t('discoverModels')
          }}</UButton
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
  </div>
</template>
