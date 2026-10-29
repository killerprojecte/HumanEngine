<script setup lang="ts">
import type { components } from '~/types/api'
type Plan = {
  mutationVersion: number
  ref: { id: string; version: number; deletionEpoch: number }
  terms?: {
    title: string
    conditions: string[]
    polarity: string
    modality: string
    temporalScope: string
    timeQualification?: string
    occurredAt?: string
    validFrom?: string
    validUntil?: string
  }
  recordedAt?: string
  confirmation: string
  lifecycle: string
  changed: boolean
  participants: Array<{
    personId: string
    displayName?: string
    confirmed: boolean
    missing: boolean
    completed: boolean
    cancelled: boolean
  }>
}
const props = defineProps<{ scenes: components['schemas']['Scene'][] }>()
const { t, locale } = useI18n(),
  { authenticated, authenticatedFetch } = useApi()
const lifecycle = ref('open')
const historyKey = ref(''),
  historyRows = ref<Plan[]>([]),
  historyBusy = ref(false),
  historyError = ref(false)
let historyController: AbortController | undefined,
  historyEpoch = 0
function clearHistory() {
  historyEpoch++
  historyController?.abort()
  historyKey.value = ''
  historyRows.value = []
  historyBusy.value = false
  historyError.value = false
}
async function loadHistory(row: Plan) {
  const key = row.ref.id + ':' + row.ref.version
  if (historyKey.value === key) {
    clearHistory()
    return
  }
  clearHistory()
  historyKey.value = key
  const current = historyEpoch
  historyController = new AbortController()
  historyBusy.value = true
  try {
    const response = await authenticatedFetch(
      `/api/memory-commitments/${encodeURIComponent(row.ref.id)}/history?${new URLSearchParams({ sceneId: sceneId.value, version: String(row.ref.version), deletionEpoch: String(row.ref.deletionEpoch) })}`,
      { signal: historyController.signal },
    )
    if (!response.ok) throw Error('unavailable')
    const value = await response.json()
    if (disposed || current !== historyEpoch || !authenticated.value) return
    historyRows.value = value.revisions
  } catch {
    if (current === historyEpoch && authenticated.value) historyError.value = true
  } finally {
    if (current === historyEpoch) historyBusy.value = false
  }
}
const sceneId = ref(''),
  personId = ref(''),
  items = ref<Plan[]>([]),
  busy = ref(false),
  error = ref(false),
  enabled = ref(false),
  limited = ref(false)
let controller: AbortController | undefined,
  epoch = 0,
  disposed = false
const scenes = computed(() => props.scenes.filter((s) => ['group', 'private'].includes(s.kind)))
const people = computed(() => [
  ...new Map(items.value.flatMap((row) => row.participants).map((p) => [p.personId, p])).values(),
])
const visible = computed(() =>
  items.value.filter(
    (row) => !personId.value || row.participants.some((p) => p.personId === personId.value),
  ),
)
const date = (value?: string) =>
  value && Number.isFinite(Date.parse(value))
    ? new Date(value).toLocaleString(locale.value)
    : t('mpUnknownTime')
async function load() {
  clearHistory()
  const current = ++epoch
  controller?.abort()
  controller = new AbortController()
  items.value = []
  personId.value = ''
  error.value = false
  enabled.value = false
  limited.value = false
  busy.value = false
  if (disposed || !authenticated.value || !sceneId.value) return
  busy.value = true
  try {
    const response = await authenticatedFetch(
      `/api/memory-commitments?${new URLSearchParams({ sceneId: sceneId.value, lifecycle: lifecycle.value })}`,
      { signal: controller.signal },
    )
    if (!response.ok) throw Error('request')
    const data = await response.json()
    if (disposed || current !== epoch || !authenticated.value) return
    items.value = data.items
    enabled.value = data.enabled
    limited.value = data.coverage === 'limited'
  } catch {
    if (current === epoch && authenticated.value) error.value = true
  } finally {
    if (current === epoch) busy.value = false
  }
}
watch([sceneId, lifecycle, authenticated], () => void load())
watch(scenes, (rows) => {
  if (sceneId.value && !rows.some((s) => s.id === sceneId.value)) sceneId.value = ''
})
watch(personId, clearHistory)
onBeforeUnmount(() => {
  clearHistory()
  disposed = true
  epoch++
  controller?.abort()
})
</script>
<template>
  <section class="rounded-lg border border-default p-4 space-y-3">
    <h2 class="font-semibold">{{ t('cmTitle') }}</h2>
    <p class="text-sm text-muted">{{ t('cmHelp') }}</p>
    <div class="flex flex-wrap gap-2">
      <select
        v-model="sceneId"
        :disabled="!authenticated"
        :aria-label="t('mpScene')"
        class="rounded border border-default bg-default p-2"
      >
        <option value="">{{ t('mpChooseScene') }}</option>
        <option v-for="scene in scenes" :key="scene.id" :value="scene.id">
          {{ scene.name || t('mpUnnamedScene') }}
        </option>
      </select>
      <select
        v-model="lifecycle"
        :disabled="!authenticated"
        :aria-label="t('cmLifecycleFilter')"
        class="rounded border border-default bg-default p-2"
      >
        <option v-for="state in ['open', 'terminal', 'all']" :key="state" :value="state">
          {{ t('cmFilter_' + state) }}
        </option>
      </select>
      <UButton variant="outline" :disabled="busy || !authenticated || !sceneId" @click="load">{{
        t('refresh')
      }}</UButton>
    </div>
    <label v-if="people.length" class="block text-sm"
      >{{ t('mrPersonFilter')
      }}<select v-model="personId" class="ml-2 rounded border border-default bg-default p-2">
        <option value="">{{ t('mrAllPeople') }}</option>
        <option v-for="(person, index) in people" :key="person.personId" :value="person.personId">
          {{ person.displayName || t('mpUnnamed', { number: index + 1 }) }}
        </option>
      </select></label
    >
    <p v-if="!authenticated">{{ t('mpSignIn') }}</p>
    <p v-if="busy" role="status">{{ t('uxLoading') }}</p>
    <p v-if="error" role="alert" class="text-error">{{ t('networkError') }}</p>
    <p v-if="sceneId && !busy && !error && !enabled">{{ t('cmDisabled') }}</p>
    <p v-else-if="sceneId && !busy && !error && !items.length">{{ t('cmEmpty') }}</p>
    <p v-if="limited" class="text-sm text-muted">{{ t('mrLimited') }}</p>
    <article
      v-for="row in visible"
      :key="row.ref.id + ':' + row.ref.version"
      class="rounded border border-default p-3 space-y-2"
    >
      <div class="flex gap-2 flex-wrap text-xs">
        <span>{{ t('cmConfirmation_' + row.confirmation) }}</span
        ><span>{{ t('cmLifecycle_' + row.lifecycle) }}</span
        ><span>{{ t('cmVersion', { version: row.ref.version }) }}</span>
      </div>
      <p v-if="row.changed" class="text-sm text-muted">{{ t('cmChanged') }}</p>
      <h3 class="font-medium">{{ row.terms?.title || t('cmTermsUnavailable') }}</h3>
      <template v-if="row.terms"
        ><p>
          {{ t('cmPolarity_' + row.terms.polarity) }} ·
          {{ t('mrModality_' + row.terms.modality) }} ·
          {{ t('mrTemporal_' + row.terms.temporalScope) }}
        </p>
        <p class="text-sm">{{ t('mrConditions') }}</p>
        <ul v-if="row.terms.conditions.length" class="list-disc pl-5">
          <li
            v-for="(condition, index) in row.terms.conditions"
            :key="index"
            class="whitespace-pre-wrap break-words"
          >
            {{ condition }}
          </li>
        </ul>
        <p v-else class="text-sm">{{ t('cmNoConditions') }}</p>
        <p class="whitespace-pre-wrap break-words">
          {{ t('cmTimeQualification') }}: {{ row.terms.timeQualification || t('mpUnknownTime') }}
        </p>
        <dl class="grid sm:grid-cols-3 gap-2 text-xs">
          <div>
            <dt>{{ t('mpOccurred') }}</dt>
            <dd>{{ date(row.terms.occurredAt) }}</dd>
          </div>
          <div>
            <dt>{{ t('mpValidFrom') }}</dt>
            <dd>{{ date(row.terms.validFrom) }}</dd>
          </div>
          <div>
            <dt>{{ t('mpValidUntil') }}</dt>
            <dd>{{ date(row.terms.validUntil) }}</dd>
          </div>
        </dl></template
      >
      <ul class="space-y-1 text-sm">
        <li v-for="(person, index) in row.participants" :key="person.personId">
          {{ person.displayName || t('mpUnnamed', { number: index + 1 }) }} ·
          {{ t(person.confirmed ? 'cmPersonConfirmed' : 'cmPersonMissing')
          }}<span v-if="person.completed"> · {{ t('cmPersonCompleted') }}</span
          ><span v-if="person.cancelled"> · {{ t('cmPersonCancelled') }}</span>
        </li>
      </ul>
      <p class="text-xs text-muted">{{ t('cmDeclared') }} {{ t('cmSourceUnavailable') }}</p>
      <p class="text-xs">{{ t('cmRecorded') }}: {{ date(row.recordedAt) }}</p>
      <CommitmentTimeline
        :scene-id="sceneId"
        :anchor="{ ...row.ref, mutationVersion: row.mutationVersion }"
      />
      <UButton variant="outline" :disabled="!authenticated" @click="loadHistory(row)">{{
        t('cmPlanHistory')
      }}</UButton>
      <section
        v-if="historyKey === row.ref.id + ':' + row.ref.version"
        class="border-t border-default pt-3 space-y-3"
      >
        <p class="text-sm text-muted">
          {{ t('cmPlanHistoryHelp') }} {{ t('cmSourceUnavailable') }}
        </p>
        <p v-if="historyBusy" role="status">{{ t('uxLoading') }}</p>
        <p v-if="historyError" role="alert" class="text-error">{{ t('cmHistoryUnavailable') }}</p>
        <p v-if="!historyBusy && !historyError && !historyRows.length">{{ t('cmHistoryEmpty') }}</p>
        <p class="text-xs text-muted">{{ t('cmHistoryLimited') }}</p>
        <article
          v-for="past in historyRows"
          :key="past.ref.version"
          class="rounded border border-default p-3 space-y-2"
        >
          <p class="text-xs">
            {{ t('cmVersion', { version: past.ref.version }) }} · {{ t('cmSavedState') }}:
            {{ t('cmConfirmation_' + past.confirmation) }} /
            {{ t('cmLifecycle_' + past.lifecycle) }}
          </p>
          <h4 class="font-medium">{{ past.terms?.title || t('cmTermsUnavailable') }}</h4>
          <template v-if="past.terms"
            ><p>
              {{ t('cmPolarity_' + past.terms.polarity) }} ·
              {{ t('mrModality_' + past.terms.modality) }} ·
              {{ t('mrTemporal_' + past.terms.temporalScope) }}
            </p>
            <p>{{ t('mrConditions') }}</p>
            <ul v-if="past.terms.conditions.length" class="list-disc pl-5">
              <li
                v-for="(condition, index) in past.terms.conditions"
                :key="index"
                class="whitespace-pre-wrap break-words"
              >
                {{ condition }}
              </li>
            </ul>
            <p v-else>{{ t('cmNoConditions') }}</p>
            <p>
              {{ t('cmTimeQualification') }}:
              {{ past.terms.timeQualification || t('mpUnknownTime') }}
            </p>
            <dl class="grid sm:grid-cols-3 gap-2 text-xs">
              <div>
                <dt>{{ t('mpOccurred') }}</dt>
                <dd>{{ date(past.terms.occurredAt) }}</dd>
              </div>
              <div>
                <dt>{{ t('mpValidFrom') }}</dt>
                <dd>{{ date(past.terms.validFrom) }}</dd>
              </div>
              <div>
                <dt>{{ t('mpValidUntil') }}</dt>
                <dd>{{ date(past.terms.validUntil) }}</dd>
              </div>
            </dl></template
          >
          <ul class="text-sm">
            <li v-for="(person, index) in past.participants" :key="person.personId">
              {{ person.displayName || t('mpUnnamed', { number: index + 1 }) }} ·
              {{ t(person.confirmed ? 'cmPersonConfirmed' : 'cmPersonMissing')
              }}<span v-if="person.completed"> · {{ t('cmPersonCompleted') }}</span
              ><span v-if="person.cancelled"> · {{ t('cmPersonCancelled') }}</span>
            </li>
          </ul>
          <p class="text-xs">{{ t('cmRecorded') }}: {{ date(past.recordedAt) }}</p>
          <p class="text-xs text-muted">{{ t('cmDeclared') }}</p>
        </article>
      </section>
    </article>
  </section>
</template>
