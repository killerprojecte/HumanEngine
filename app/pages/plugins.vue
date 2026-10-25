<script setup lang="ts">
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
const selected = computed(() => plugins.value.find((plugin) => plugin.id === selectedId.value))
function object(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {}
}
const fields = computed<Field[]>(() => {
  const schema = selected.value?.configSchema ?? {},
    required = Array.isArray(schema.required) ? schema.required : []
  return Object.entries(object(schema.properties)).map(([key, raw]) => {
    const entry = object(raw)
    return {
      key,
      title: typeof entry.title === 'string' ? entry.title : key,
      description: typeof entry.description === 'string' ? entry.description : '',
      type: typeof entry.type === 'string' ? entry.type : 'string',
      required: required.includes(key),
      secret: entry.secret === true,
      group: typeof entry.group === 'string' ? entry.group : t('pluginGeneral'),
      ...(Array.isArray(entry.enum)
        ? { options: entry.enum.map((value) => ({ label: String(value), value: String(value) })) }
        : {}),
      minimum: typeof entry.minimum === 'number' ? entry.minimum : undefined,
      maximum: typeof entry.maximum === 'number' ? entry.maximum : undefined,
      defaultValue: entry.default,
    }
  })
})
const groups = computed(() => [...new Set(fields.value.map((field) => field.group))])
const dirty = computed(() => JSON.stringify(values.value) !== JSON.stringify(original.value))
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
    else result[field.key] = typeof value === 'string' || typeof value === 'number' ? value : ''
  }
  return result
}
async function list() {
  const response = await client.GET('/api/plugins')
  if (!response.data) throw new Error(response.error?.error ?? t('networkError'))
  plugins.value = response.data
}
async function load() {
  loading.value = true
  error.value = ''
  try {
    await list()
  } catch {
    error.value = t('networkError')
  } finally {
    loading.value = false
  }
}
async function select(id: string) {
  if (busy.value || loading.value || dirty.value) return
  loading.value = true
  error.value = ''
  saved.value = false
  selectedId.value = id
  try {
    const response = await client.GET('/api/plugins/{id}/config', { params: { path: { id } } })
    if (response.data) {
      values.value = formValues(response.data)
      original.value = structuredClone(values.value)
    } else {
      error.value = response.error?.error ?? t('networkError')
      selectedId.value = undefined
    }
  } catch {
    error.value = t('networkError')
    selectedId.value = undefined
  } finally {
    loading.value = false
  }
}
function reset() {
  if (!busy.value) {
    values.value = structuredClone(original.value)
    error.value = ''
    saved.value = false
  }
}
function configBody(): Record<string, unknown> {
  const body: Record<string, unknown> = {}
  for (const field of fields.value) {
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
  if (busy.value || loading.value || !selected.value) return
  let body: Record<string, unknown>
  try {
    body = configBody()
  } catch (failure) {
    error.value = failure instanceof Error ? failure.message : t('networkError')
    return
  }
  busy.value = true
  error.value = ''
  saved.value = false
  try {
    const response = await client.PUT('/api/plugins/{id}/config', {
      params: { path: { id: selected.value.id } },
      body,
    })
    if (!response.data) {
      error.value = response.error?.error ?? t('networkError')
      return
    }
    values.value = formValues(response.data)
    original.value = structuredClone(values.value)
    saved.value = true
    await list()
  } catch {
    error.value = t('networkError')
  } finally {
    busy.value = false
  }
}
async function setEnabled(plugin: Plugin, enabled: boolean, reload = false) {
  if (busy.value || loading.value || dirty.value) return
  busy.value = true
  error.value = ''
  saved.value = false
  try {
    if (reload) {
      const disabled = await client.PUT('/api/plugins/{id}/enabled', {
        params: { path: { id: plugin.id } },
        body: { enabled: false },
      })
      if (!disabled.data) throw new Error('disable_failed')
    }
    const response = await client.PUT('/api/plugins/{id}/enabled', {
      params: { path: { id: plugin.id } },
      body: { enabled },
    })
    if (!response.data) error.value = response.error?.error ?? t('networkError')
    await list()
  } catch {
    error.value = t('networkError')
    try {
      await list()
    } catch {
      /* Preserve the original error. */
    }
  } finally {
    busy.value = false
  }
}
onMounted(load)
</script>
<template>
  <div class="space-y-6">
    <header>
      <h1 class="text-2xl font-semibold">{{ t('pluginsTitle') }}</h1>
      <p class="text-muted mt-1">{{ t('pluginsHelp') }}</p>
    </header>
    <UAlert color="warning" :title="t('pluginsTrust')" /><UAlert
      v-if="error"
      color="error"
      :title="error"
    /><UAlert v-if="saved" color="success" :title="t('pluginSaved')" /><UAlert
      v-if="dirty"
      color="info"
      :title="t('pluginUnsaved')"
    />
    <div class="grid lg:grid-cols-[minmax(240px,1fr)_2fr] gap-6">
      <div class="space-y-4">
        <UCard
          v-for="plugin in plugins"
          :key="plugin.id"
          :class="selectedId === plugin.id ? 'ring-2 ring-primary' : ''"
          ><div class="space-y-3">
            <div class="flex justify-between gap-2">
              <h2 class="font-semibold">{{ plugin.name }}</h2>
              <UBadge :color="plugin.enabled ? 'success' : 'neutral'" variant="soft">{{
                t(plugin.enabled ? 'enabled' : 'disabled')
              }}</UBadge>
            </div>
            <p class="text-sm text-muted">{{ plugin.description }}</p>
            <p class="text-xs text-muted">v{{ plugin.version }} · {{ plugin.id }}</p>
            <div class="flex flex-wrap gap-1">
              <UBadge
                v-for="capability in plugin.capabilities"
                :key="capability"
                color="neutral"
                variant="outline"
                size="sm"
                >{{ capability }}</UBadge
              >
            </div>
            <UAlert v-if="plugin.error" color="error" :title="plugin.error" />
            <div class="flex flex-wrap gap-2">
              <UButton
                size="sm"
                variant="soft"
                :disabled="busy || loading || dirty"
                @click="select(plugin.id)"
                >{{ t('configure') }}</UButton
              ><UButton
                size="sm"
                variant="ghost"
                :disabled="busy || loading || dirty"
                @click="setEnabled(plugin, !plugin.enabled)"
                >{{ t(plugin.enabled ? 'disable' : 'enable') }}</UButton
              ><UButton
                v-if="plugin.enabled"
                size="sm"
                variant="ghost"
                icon="i-lucide-refresh-cw"
                :disabled="busy || loading || dirty"
                @click="setEnabled(plugin, true, true)"
                >{{ t('pluginReload') }}</UButton
              >
            </div>
          </div></UCard
        >
        <p v-if="!loading && !plugins.length" class="text-muted">{{ t('pluginsEmpty') }}</p>
      </div>
      <UCard v-if="selected"
        ><template #header
          ><h2 class="font-semibold">{{ selected.name }} · {{ t('configure') }}</h2></template
        >
        <form class="space-y-5" @submit.prevent="save">
          <p class="text-sm text-muted">{{ t('pluginReloadHelp') }}</p>
          <fieldset
            v-for="group in groups"
            :key="group"
            :disabled="busy || loading"
            class="space-y-4"
          >
            <legend class="font-medium mb-3">{{ group }}</legend>
            <UFormField
              v-for="field in fields.filter((item) => item.group === group)"
              :key="field.key"
              :label="field.title"
              :description="field.description"
              :required="field.required"
              :help="
                field.secret
                  ? t('pluginSecretHelp')
                  : field.key === 'credentialEnv'
                    ? t('credentialHelp')
                    : undefined
              "
              ><USelect
                v-if="field.options"
                :model-value="String(values[field.key] ?? '')"
                :items="field.options"
                class="w-full"
                @update:model-value="(value) => setValue(field.key, value)" /><USwitch
                v-else-if="field.type === 'boolean'"
                :model-value="values[field.key] === true"
                @update:model-value="(value) => setValue(field.key, value)" /><UTextarea
                v-else-if="field.type === 'object' || field.type === 'array'"
                :model-value="String(values[field.key] ?? '')"
                :rows="5"
                class="w-full font-mono"
                @update:model-value="(value) => setValue(field.key, value)" /><UInput
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
            /></UFormField>
          </fieldset>
          <div class="flex gap-2">
            <UButton type="submit" :loading="busy" :disabled="loading">{{ t('save') }}</UButton
            ><UButton variant="ghost" :disabled="busy || loading || !dirty" @click="reset">{{
              t('resetChanges')
            }}</UButton>
          </div>
        </form>
        <div v-if="safePages.length" class="mt-6 pt-5 border-t border-default space-y-3">
          <h3 class="font-medium">{{ t('pluginCustomPages') }}</h3>
          <UButton
            v-for="page in safePages"
            :key="page.id"
            :href="page.entry"
            target="_blank"
            rel="noopener noreferrer"
            variant="soft"
            icon="i-lucide-external-link"
            class="mr-2"
            >{{ page.title }}</UButton
          >
        </div></UCard
      ><UCard v-else
        ><p class="text-muted">{{ t('pluginChoose') }}</p></UCard
      >
    </div>
  </div>
</template>
