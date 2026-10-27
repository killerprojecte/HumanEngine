<script setup lang="ts">
import type { components } from '~/types/api'
type Settings = components['schemas']['Settings']
const { t } = useI18n(),
  { client } = useApi()
const learning = ref<components['schemas']['LearningStatus']>()
const learningBusy = ref(false),
  learningError = ref('')
async function loadLearning() {
  if (learningBusy.value) return
  learningBusy.value = true
  learningError.value = ''
  try {
    const result = await client.GET('/api/learning/status')
    if (!result.data) throw new Error(result.error?.error ?? t('networkError'))
    learning.value = result.data
  } catch (cause) {
    learningError.value = cause instanceof Error ? cause.message : t('networkError')
  } finally {
    learningBusy.value = false
  }
}
async function actLearning(id: string, retry: boolean) {
  if (learningBusy.value) return
  if (!retry && !window.confirm(t('stLearningDiscardConfirm'))) return
  learningBusy.value = true
  learningError.value = ''
  try {
    const result = retry
      ? await client.POST('/api/learning/failures/{id}/retry', { params: { path: { id } } })
      : await client.DELETE('/api/learning/failures/{id}', { params: { path: { id } } })
    if (!result.data) throw new Error(result.error?.error ?? t('networkError'))
  } catch (cause) {
    learningError.value = cause instanceof Error ? cause.message : t('networkError')
  } finally {
    learningBusy.value = false
  }
  if (!learningError.value) await loadLearning()
}
const settings = ref<Settings>(),
  busy = ref(false),
  error = ref(''),
  saved = ref(false)
const opening = ref(''),
  uncertain = ref(false),
  reconciled = ref(false),
  confirmation = ref(false)
const freshSaved = ref<Settings>()
const confirmationDescription = ref('')
function showConfirmation() {
  confirmationDescription.value = t(
    uncertain.value
      ? 'stDiscardUncertainHelp'
      : reconciled.value
        ? 'stDiscardLatestHelp'
        : 'stDiscardHelp',
  )
  confirmation.value = true
}
const dirty = computed(() => !!settings.value && JSON.stringify(settings.value) !== opening.value)
const locked = computed(() => busy.value || uncertain.value)
let pendingNavigation: ((allow: boolean) => void) | undefined
async function load(recovery = false) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    const r = await client.GET('/api/settings')
    if (!r.data) throw new Error(r.error?.error ?? t('networkError'))
    if (recovery) {
      freshSaved.value = r.data
      reconciled.value = true
    } else {
      settings.value = r.data
      opening.value = JSON.stringify(r.data)
    }
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : t('networkError')
  } finally {
    busy.value = false
  }
}
function askDiscard() {
  if (busy.value || (uncertain.value && !reconciled.value)) return
  showConfirmation()
}
function cancelDiscard() {
  confirmation.value = false
  pendingNavigation?.(false)
  pendingNavigation = undefined
}
function discard() {
  if (pendingNavigation && uncertain.value && !reconciled.value) {
    confirmation.value = false
    pendingNavigation(true)
    pendingNavigation = undefined
    return
  }
  settings.value = freshSaved.value
    ? JSON.parse(JSON.stringify(freshSaved.value))
    : JSON.parse(opening.value)
  opening.value = JSON.stringify(settings.value)
  uncertain.value = false
  reconciled.value = false
  freshSaved.value = undefined
  confirmation.value = false
  error.value = ''
  saved.value = false
  pendingNavigation?.(true)
  pendingNavigation = undefined
}
function addWindow() {
  if (!settings.value || locked.value || settings.value.participationWindows.length >= 12) return
  settings.value.participationWindows.push({ startHour: 9, endHour: 18, inclination: 'balanced' })
}
async function save() {
  if (!settings.value || locked.value || !dirty.value) return
  if (settings.value.participationWindows.some((w) => w.startHour >= w.endHour)) {
    error.value = t('stWindowInvalid')
    return
  }
  try {
    new Intl.DateTimeFormat('en', { timeZone: settings.value.timezone })
  } catch {
    error.value = t('stTimezoneInvalid')
    return
  }
  busy.value = true
  error.value = ''
  saved.value = false
  let submitted = false
  try {
    const latest = await client.GET('/api/settings')
    if (!latest.data) throw new Error(latest.error?.error ?? t('networkError'))
    if (JSON.stringify(latest.data) !== opening.value) {
      freshSaved.value = latest.data
      reconciled.value = true
      uncertain.value = true
      error.value = t('stConflict')
      return
    }
    submitted = true
    const result = await client.PUT('/api/settings', {
      body: JSON.parse(JSON.stringify(settings.value)),
    })
    if (!result.data) {
      if (result.response.status >= 400 && result.response.status < 500) submitted = false
      throw new Error(result.error?.error ?? t('networkError'))
    }
    settings.value = result.data
    opening.value = JSON.stringify(result.data)
    saved.value = true
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : t('networkError')
    if (submitted) {
      uncertain.value = true
      reconciled.value = false
    }
  } finally {
    busy.value = false
  }
}
onBeforeRouteLeave(() => {
  if (busy.value) return false
  if (!dirty.value && !uncertain.value) return true
  showConfirmation()
  pendingNavigation?.(false)
  return new Promise<boolean>((resolve) => {
    pendingNavigation = resolve
  })
})
function warnBeforeUnload(event: BeforeUnloadEvent) {
  if (!dirty.value && !busy.value && !uncertain.value) return
  event.preventDefault()
  event.returnValue = ''
}
onMounted(() => {
  void load()
  void loadLearning()
  window.addEventListener('beforeunload', warnBeforeUnload)
})
onBeforeUnmount(() => {
  pendingNavigation?.(false)
  window.removeEventListener('beforeunload', warnBeforeUnload)
})
const timingFields = [
  {
    key: 'learningQueueMaxMessages' as const,
    label: 'stQueueScene',
    help: 'stQueueSceneHelp',
    min: 60,
    max: 20000,
  },
  {
    key: 'learningQueueMaxTotalMessages' as const,
    label: 'stQueueTotal',
    help: 'stQueueTotalHelp',
    min: 60,
    max: 100000,
  },
  {
    key: 'learningRetryAttempts' as const,
    label: 'stRetryAttempts',
    help: 'stRetryAttemptsHelp',
    min: 1,
    max: 5,
  },
  {
    key: 'learningRetryBaseMs' as const,
    label: 'stRetryBase',
    help: 'stRetryBaseHelp',
    min: 100,
    max: 30000,
  },
  { key: 'debounceMs' as const, label: 'stDebounce', help: 'stDebounceHelp', min: 0, max: 30000 },
  { key: 'maxWaitMs' as const, label: 'stMaxWait', help: 'stMaxWaitHelp', min: 100, max: 60000 },
  {
    key: 'learningBatchMs' as const,
    label: 'stLearningDelay',
    help: 'stLearningDelayHelp',
    min: 100,
    max: 300000,
  },
  {
    key: 'modelConcurrency' as const,
    label: 'stConcurrency',
    help: 'stConcurrencyHelp',
    min: 1,
    max: 32,
  },
]
</script>
<template>
  <div class="settings-page space-y-5 max-w-5xl">
    <header>
      <h1 class="page-heading">{{ t('stTitle') }}</h1>
      <p class="mt-2 text-sm text-muted">{{ t('stIntro') }}</p>
    </header>
    <section class="settings-section">
      <div class="settings-heading">
        <h2>{{ t('stLearningStatus') }}</h2>
        <UButton
          :loading="learningBusy"
          :disabled="learningBusy"
          variant="outline"
          @click="loadLearning"
          >{{ t('refresh') }}</UButton
        >
      </div>
      <div class="settings-body">
        <p class="text-sm text-muted">{{ t('stLearningStatusHelp') }}</p>
        <UAlert v-if="learningError" color="error" :title="learningError" />
        <dl v-if="learning" class="grid grid-cols-2 sm:grid-cols-3 gap-3 text-sm">
          <div
            v-for="key in [
              'accepted',
              'completed',
              'failed',
              'overflow',
              'retried',
              'queued',
              'retrying',
              'active',
              'paused',
            ] as const"
            :key="key"
          >
            <dt class="text-muted">{{ t(`stLearning_${key}`) }}</dt>
            <dd>{{ learning[key] }}</dd>
          </div>
        </dl>
        <div
          v-for="failure in learning?.failures ?? []"
          :key="failure.id"
          class="rounded border border-warning/40 p-3 space-y-2 text-sm"
        >
          <p class="break-all">{{ failure.sceneId }}</p>
          <p>
            {{ t('stLearningFailure', { count: failure.messageCount, attempt: failure.attempt }) }}
          </p>
          <p v-if="failure.partial" class="text-warning">{{ t('stLearningPartial') }}</p>
          <div class="flex gap-2">
            <UButton
              v-if="failure.canRetry"
              :disabled="learningBusy"
              @click="actLearning(failure.id, true)"
              >{{ t('stLearningRetry') }}</UButton
            ><UButton
              variant="outline"
              color="error"
              :disabled="learningBusy"
              @click="actLearning(failure.id, false)"
              >{{ t('stLearningDiscard') }}</UButton
            >
          </div>
        </div>
      </div>
    </section>
    <UAlert v-if="error" color="error" :title="error" />
    <UAlert v-if="saved && !dirty" color="success" :title="t('stSaved')" />
    <section
      v-if="uncertain"
      class="rounded-lg border border-warning/40 bg-warning/5 p-4 space-y-3"
      aria-live="polite"
    >
      <h2 class="font-semibold text-sm">{{ t('stReviewTitle') }}</h2>
      <p class="text-sm leading-6">{{ t(reconciled ? 'stReconciled' : 'stUncertain') }}</p>
      <div class="flex flex-wrap gap-2">
        <UButton
          variant="outline"
          color="neutral"
          :loading="busy"
          :disabled="busy"
          @click="load(true)"
          >{{ t('stReadSaved') }}</UButton
        ><UButton v-if="reconciled" :disabled="busy" @click="askDiscard">{{
          t('stUseSaved')
        }}</UButton>
      </div>
    </section>
    <div v-if="!settings" class="rounded-lg border border-default p-8 text-center text-muted">
      <p>{{ t(busy ? 'uxLoading' : 'networkError') }}</p>
      <UButton v-if="!busy" class="mt-3" @click="load()">{{ t('refresh') }}</UButton>
    </div>
    <form v-else class="space-y-5" @submit.prevent="save">
      <fieldset :disabled="locked" class="space-y-5 min-w-0">
        <section id="identity" class="settings-section">
          <div class="settings-heading">
            <h2>{{ t('stIdentity') }}</h2>
            <NuxtLink to="/rules" class="text-sm text-primary hover:underline">{{
              t('stHistoryLink')
            }}</NuxtLink>
          </div>
          <div class="settings-body">
            <div class="grid sm:grid-cols-2 gap-5">
              <UFormField :label="t('name')" :help="t('stNameHelp')" required
                ><UInput
                  v-model="settings.name"
                  required
                  maxlength="100"
                  class="w-full" /></UFormField
              ><UFormField :label="t('timezone')" :help="t('stTimezoneHelp')" required
                ><UInput
                  v-model="settings.timezone"
                  required
                  placeholder="Asia/Shanghai"
                  class="w-full"
              /></UFormField>
            </div>
            <UFormField :label="t('persona')" :help="t('stPersonaHelp')" required
              ><UTextarea
                v-model="settings.persona"
                required
                maxlength="20000"
                :rows="6"
                class="w-full"
            /></UFormField>
          </div>
        </section>
        <section class="settings-section">
          <div class="settings-heading">
            <h2>{{ t('stParticipation') }}</h2>
            <UButton
              type="button"
              variant="outline"
              color="neutral"
              size="sm"
              :disabled="locked || settings.participationWindows.length >= 12"
              @click="addWindow"
              >{{ t('stAddWindow') }}</UButton
            >
          </div>
          <div class="settings-body">
            <p class="text-sm leading-6 text-muted">{{ t('stWindowsHelp') }}</p>
            <p class="text-xs text-muted">
              {{ t('timezone') }}:
              <strong class="font-medium text-default">{{ settings.timezone }}</strong> ·
              {{ t('stWindowOrder') }}
            </p>
            <p
              v-if="!settings.participationWindows.length"
              class="rounded-md bg-elevated p-4 text-sm text-muted"
            >
              {{ t('stNoWindows') }}
            </p>
            <div
              v-for="(window, index) in settings.participationWindows"
              :key="index"
              class="window-row grid grid-cols-2 sm:grid-cols-[1fr_1fr_1.5fr_auto] gap-3 items-start rounded-md border border-default p-3"
            >
              <UFormField :label="t('stStartHour')"
                ><UInput
                  v-model.number="window.startHour"
                  required
                  type="number"
                  min="0"
                  max="23"
                  step="1"
                  class="w-full"
              /></UFormField>
              <UFormField :label="t('stEndHour')"
                ><UInput
                  v-model.number="window.endHour"
                  required
                  type="number"
                  min="1"
                  max="24"
                  step="1"
                  class="w-full"
              /></UFormField>
              <UFormField :label="t('windowInclination')"
                ><USelect
                  v-model="window.inclination"
                  :items="[
                    { label: t('inclinationQuiet'), value: 'quiet' },
                    { label: t('inclinationBalanced'), value: 'balanced' },
                    { label: t('inclinationEngaged'), value: 'engaged' },
                  ]"
                  class="w-full"
              /></UFormField>
              <UButton
                type="button"
                variant="ghost"
                color="error"
                class="mt-6 justify-center"
                :aria-label="t('stRemoveWindow', { index: index + 1 })"
                @click="settings.participationWindows.splice(index, 1)"
                >{{ t('remove') }}</UButton
              >
            </div>
            <NuxtLink to="/scenes" class="inline-block text-sm text-primary hover:underline">{{
              t('stSceneLink')
            }}</NuxtLink>
          </div>
        </section>
        <details class="settings-section">
          <summary class="settings-heading cursor-pointer font-semibold text-base">
            {{ t('stAdvanced') }}
          </summary>
          <div class="settings-body grid sm:grid-cols-2 gap-5">
            <UFormField
              v-for="field in timingFields"
              :key="field.key"
              :label="t(field.label)"
              :help="t(field.help)"
              ><UInput
                v-model.number="settings[field.key]"
                type="number"
                required
                step="1"
                :min="field.min"
                :max="field.max"
                class="w-full"
            /></UFormField>
          </div>
        </details>
        <section class="settings-section">
          <div class="settings-heading">
            <h2>{{ t('stTemporary') }}</h2>
          </div>
          <div class="settings-body">
            <div class="space-y-3">
              <USwitch
                id="settings-cache-enabled"
                v-model="settings.cacheEnabled"
                :label="t('stCacheLabel')"
              />
              <p class="text-sm text-muted leading-6">{{ t('stCacheHelp') }}</p>
              <UFormField :label="t('stCacheHours')" :help="t('stCacheHoursHelp')" class="max-w-xs"
                ><UInput
                  v-model.number="settings.cacheHours"
                  type="number"
                  required
                  min="0.1"
                  max="168"
                  step="any"
                  :disabled="!settings.cacheEnabled || locked"
                  class="w-full"
              /></UFormField>
            </div>
            <div id="diagnostics" class="border-t border-default pt-5 space-y-3">
              <USwitch
                id="settings-debug-io"
                v-model="settings.debugModelIO"
                :label="t('stDiagnosticsLabel')"
              />
              <p class="text-sm leading-6 text-muted">{{ t('stDiagnosticsHelp') }}</p>
              <p class="rounded-md border border-warning/30 bg-warning/5 p-3 text-sm leading-6">
                {{ t('stDiagnosticsWarning') }}
              </p>
              <NuxtLink to="/logs" class="inline-block text-sm text-primary hover:underline">{{
                t('stLogsLink')
              }}</NuxtLink>
            </div>
          </div>
        </section>
      </fieldset>
      <footer
        class="sticky bottom-0 z-10 rounded-lg border border-default bg-default p-4 flex flex-wrap gap-3 items-center justify-between shadow-sm"
      >
        <p class="text-sm text-muted">{{ t(dirty ? 'stDraftNotice' : 'stNoChanges') }}</p>
        <div class="flex gap-2">
          <UButton
            type="button"
            variant="outline"
            color="neutral"
            :disabled="busy || (!dirty && !uncertain) || (uncertain && !reconciled)"
            @click="askDiscard"
            >{{ t('stDiscard') }}</UButton
          ><UButton type="submit" :loading="busy" :disabled="locked || !dirty">{{
            t('stSave')
          }}</UButton>
        </div>
      </footer>
    </form>
    <UModal
      :open="confirmation"
      :title="t('stDiscardTitle')"
      :description="confirmationDescription"
      :dismissible="!busy"
      :ui="{ content: 'max-w-md' }"
      @update:open="
        (value) => {
          if (!value) cancelDiscard()
        }
      "
      ><template #footer
        ><div class="flex justify-end gap-2 w-full">
          <UButton color="neutral" variant="outline" @click="cancelDiscard">{{
            t('cancel')
          }}</UButton
          ><UButton color="warning" @click="discard">{{ t('stDiscard') }}</UButton>
        </div></template
      ></UModal
    >
  </div>
</template>
<style scoped>
.settings-section {
  border: 1px solid var(--ui-border);
  border-radius: 8px;
  background: var(--ui-bg);
}
.settings-heading {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 16px 20px;
}
.settings-heading h2 {
  font-size: 16px;
  font-weight: 600;
}
.settings-body {
  padding: 0 20px 20px;
}
.settings-body > * + * {
  margin-top: 20px;
}
.settings-body.grid > * {
  margin-top: 0;
}
@media (max-width: 640px) {
  .settings-heading {
    padding: 16px;
  }
  .settings-body {
    padding: 0 16px 16px;
  }
}
</style>
