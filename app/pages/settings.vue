<script setup lang="ts">
import type { components } from '~/types/api'
const { t } = useI18n(),
  { client } = useApi(),
  settings = ref<components['schemas']['Settings']>(),
  error = ref(''),
  saved = ref(false),
  busy = ref(false)
const lastSaved = ref('')
const dirty = computed(() => !!settings.value && JSON.stringify(settings.value) !== lastSaved.value)
function reset() {
  if (busy.value || !lastSaved.value) return
  settings.value = JSON.parse(lastSaved.value)
  error.value = ''
  saved.value = false
}
function addWindow() {
  if (!settings.value || settings.value.participationWindows.length >= 12) return
  settings.value.participationWindows.push({ startHour: 9, endHour: 18, inclination: 'balanced' })
}
onMounted(async () => {
  try {
    const r = await client.GET('/api/settings')
    settings.value = r.data
    if (r.data) lastSaved.value = JSON.stringify(r.data)
    error.value = r.error?.error ?? ''
  } catch {
    error.value = t('networkError')
  }
})
async function save() {
  if (!settings.value || busy.value) return
  busy.value = true
  saved.value = false
  try {
    const r = await client.PUT('/api/settings', { body: settings.value })
    error.value = r.error?.error ?? ''
    saved.value = !!r.data
    if (r.data) {
      settings.value = r.data
      lastSaved.value = JSON.stringify(r.data)
    }
  } catch {
    error.value = t('networkError')
  } finally {
    busy.value = false
  }
}
</script>
<template>
  <div class="space-y-6 max-w-3xl">
    <h1 class="text-2xl font-semibold">{{ t('settings') }}</h1>
    <UAlert v-if="error" color="error" :title="error" /><UAlert
      v-if="saved && !dirty"
      color="success"
      :title="t('savedNextTurn')"
    /><UCard v-if="settings"
      ><form class="space-y-5" @submit.prevent="save">
        <fieldset :disabled="busy" class="space-y-5 min-w-0">
          <UFormField :label="t('name')"
            ><UInput v-model="settings.name" class="w-full" /></UFormField
          ><UFormField :label="t('timezone')"
            ><UInput v-model="settings.timezone" class="w-full" /></UFormField
          ><UFormField :label="t('persona')" :help="t('personaHelp')"
            ><UTextarea v-model="settings.persona" :rows="8" class="w-full" /></UFormField
          ><USwitch v-model="settings.cacheEnabled" :label="t('cacheEnabled')" /><UFormField
            :label="t('cacheHours')"
            ><UInput v-model.number="settings.cacheHours" type="number" min="0.1" max="168"
          /></UFormField>
          <div class="grid grid-cols-2 gap-4">
            <UFormField :label="t('debounceMs')"
              ><UInput
                v-model.number="settings.debounceMs"
                type="number"
                min="0"
                max="30000" /></UFormField
            ><UFormField :label="t('maxWaitMs')"
              ><UInput v-model.number="settings.maxWaitMs" type="number" min="100" max="60000"
            /></UFormField>
          </div>
          <UFormField :label="t('learningBatchMs')" :help="t('learningBatchHelp')"
            ><UInput v-model.number="settings.learningBatchMs" type="number" min="100" max="300000"
          /></UFormField>
          <UFormField :label="t('modelConcurrency')" :help="t('modelConcurrencyHelp')"
            ><UInput v-model.number="settings.modelConcurrency" type="number" min="1" max="32"
          /></UFormField>
          <USwitch
            v-model="settings.debugModelIO"
            :label="t('debugModelIO')"
            :description="t('debugModelIOHelp')"
          />
          <section class="space-y-3">
            <div class="flex items-center justify-between">
              <h2 class="font-semibold">{{ t('participationWindows') }}</h2>
              <UButton
                type="button"
                variant="soft"
                :disabled="settings.participationWindows.length >= 12"
                @click="addWindow"
                >{{ t('add') }}</UButton
              >
            </div>
            <p class="text-sm text-muted">{{ t('participationWindowsHelp') }}</p>
            <div
              v-for="(window, index) in settings.participationWindows"
              :key="index"
              class="grid grid-cols-2 md:grid-cols-4 gap-3 items-end"
            >
              <UFormField :label="t('windowStart')"
                ><UInput
                  v-model.number="window.startHour"
                  type="number"
                  min="0"
                  max="23" /></UFormField
              ><UFormField :label="t('windowEnd')"
                ><UInput
                  v-model.number="window.endHour"
                  type="number"
                  min="1"
                  max="24" /></UFormField
              ><UFormField :label="t('windowInclination')"
                ><USelect
                  v-model="window.inclination"
                  :items="[
                    { label: t('inclinationQuiet'), value: 'quiet' },
                    { label: t('inclinationBalanced'), value: 'balanced' },
                    { label: t('inclinationEngaged'), value: 'engaged' },
                  ]" /></UFormField
              ><UButton
                type="button"
                variant="ghost"
                color="error"
                @click="settings.participationWindows.splice(index, 1)"
                >{{ t('remove') }}</UButton
              >
            </div>
          </section>
          <div class="flex gap-2">
            <UButton type="submit" :loading="busy">{{ t('save') }}</UButton
            ><UButton type="button" variant="ghost" :disabled="!dirty" @click="reset">{{
              t('reset')
            }}</UButton>
          </div>
        </fieldset>
      </form></UCard
    >
  </div>
</template>
