<script setup lang="ts">
import type { components } from '~/types/api'
type Rule = components['schemas']['Rule']
const { t } = useI18n(),
  { client } = useApi()
const rules = ref<Rule[]>([]),
  original = ref<Rule[]>([]),
  scenes = ref<components['schemas']['Scene'][]>([])
const versions = ref<Array<{ id: string; createdAt: string; content: string }>>([]),
  busy = ref(false),
  error = ref(''),
  saved = ref(false),
  loaded = ref(false)
const changed = computed(() => JSON.stringify(rules.value) !== JSON.stringify(original.value))
const valid = computed(() =>
  rules.value.every((rule) => rule.content.trim() && (rule.scope === 'global' || rule.sceneId)),
)
async function load() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    const [r, s, v] = await Promise.all([
      client.GET('/api/rules'),
      client.GET('/api/scenes'),
      client.GET('/api/persona/versions'),
    ])
    if (r.error || s.error || v.error) throw new Error('request_failed')
    rules.value = r.data ?? []
    original.value = structuredClone(rules.value)
    scenes.value = s.data ?? []
    versions.value = v.data ?? []
    loaded.value = true
  } catch {
    error.value = t('networkError')
  } finally {
    busy.value = false
  }
}
function add() {
  if (!busy.value) {
    rules.value.push({
      id: crypto.randomUUID(),
      scope: 'global',
      content: '',
      nonOverridable: false,
      enabled: true,
    })
    saved.value = false
  }
}
function reset() {
  if (!busy.value) {
    rules.value = structuredClone(original.value)
    saved.value = false
    error.value = ''
  }
}
async function save() {
  if (busy.value || !valid.value) return
  busy.value = true
  error.value = ''
  saved.value = false
  try {
    const body = rules.value.map((rule) => ({
      ...rule,
      content: rule.content.trim(),
      ...(rule.scope === 'global' ? { sceneId: undefined } : {}),
    }))
    const response = await client.PUT('/api/rules', { body })
    if (response.data) {
      rules.value = response.data
      original.value = structuredClone(response.data)
      saved.value = true
    } else error.value = response.error?.error ?? t('networkError')
  } catch {
    error.value = t('networkError')
  } finally {
    busy.value = false
  }
}
onMounted(load)
</script>
<template>
  <div class="space-y-6">
    <header class="flex flex-wrap justify-between items-start gap-3">
      <div>
        <h1 class="text-2xl font-semibold">{{ t('rulesTitle') }}</h1>
        <p class="text-muted mt-1">{{ t('rulesHelp') }}</p>
      </div>
      <div class="flex gap-2">
        <UButton variant="ghost" :disabled="busy || !changed" @click="reset">{{
          t('resetChanges')
        }}</UButton
        ><UButton
          :loading="busy"
          :disabled="!loaded || !valid || !changed"
          icon="i-lucide-save"
          @click="save"
          >{{ t('save') }}</UButton
        >
      </div>
    </header>
    <UAlert v-if="error" color="error" :title="error" /><UAlert
      v-if="saved"
      color="success"
      :title="t('savedNextTurn')"
    /><UAlert color="info" :title="t('rulesHierarchy')" />
    <UCard
      ><template #header
        ><div class="flex justify-between items-center">
          <h2 class="font-semibold">{{ t('manualRules') }}</h2>
          <UButton
            size="sm"
            variant="soft"
            :disabled="busy || !loaded"
            icon="i-lucide-plus"
            @click="add"
            >{{ t('add') }}</UButton
          >
        </div></template
      >
      <p v-if="loaded && !rules.length" class="text-muted">{{ t('rulesEmpty') }}</p>
      <fieldset
        v-for="rule in rules"
        :key="rule.id"
        :disabled="busy"
        class="space-y-4 py-5 border-b border-default last:border-0"
      >
        <div class="grid sm:grid-cols-2 gap-4">
          <UFormField :label="t('memoryScope')"
            ><USelect
              v-model="rule.scope"
              :items="[
                { label: t('rulesGlobal'), value: 'global' },
                { label: t('memoryScene'), value: 'scene' },
              ]"
              class="w-full" /></UFormField
          ><UFormField v-if="rule.scope === 'scene'" :label="t('scenes')" required
            ><USelect
              v-model="rule.sceneId"
              :items="scenes.map((scene) => ({ label: scene.name, value: scene.id }))"
              class="w-full"
          /></UFormField>
        </div>
        <UFormField :label="t('rulesContent')" required
          ><UTextarea v-model="rule.content" :rows="3" maxlength="5000" class="w-full"
        /></UFormField>
        <div class="flex flex-wrap items-center justify-between gap-4">
          <div class="flex flex-wrap gap-5">
            <USwitch v-model="rule.enabled" :label="t('enabled')" /><UCheckbox
              v-model="rule.nonOverridable"
              :label="t('rulesNonOverridable')"
            />
          </div>
          <UButton
            variant="ghost"
            color="error"
            size="sm"
            @click="rules = rules.filter((item) => item.id !== rule.id)"
            >{{ t('remove') }}</UButton
          >
        </div>
      </fieldset>
    </UCard>
    <UCard
      ><template #header
        ><div class="flex justify-between items-center gap-3">
          <h2 class="font-semibold">{{ t('personaVersions') }}</h2>
          <UButton to="/settings" variant="soft" size="sm">{{ t('editPersona') }}</UButton>
        </div></template
      >
      <p class="text-sm text-muted mb-4">{{ t('personaVersionsHelp') }}</p>
      <p v-if="loaded && !versions.length" class="text-muted">{{ t('personaVersionsEmpty') }}</p>
      <details
        v-for="version in [...versions].reverse()"
        :key="version.id"
        class="py-3 border-b border-default last:border-0"
      >
        <summary class="cursor-pointer font-medium">
          {{ new Date(version.createdAt).toLocaleString() }}
          <span class="text-xs text-muted">{{ version.id.slice(0, 8) }}</span>
        </summary>
        <p class="whitespace-pre-wrap break-words text-sm mt-3">{{ version.content }}</p>
      </details></UCard
    >
  </div>
</template>
