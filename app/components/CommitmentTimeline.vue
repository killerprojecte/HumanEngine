<script setup lang="ts">
const props = defineProps<{
  sceneId: string
  anchor: { id: string; version: number; deletionEpoch: number; mutationVersion: number }
}>()
const { t, locale } = useI18n(),
  { authenticatedFetch, authenticated } = useApi()
type State = {
  ref: { version: number }
  confirmation: string
  lifecycle: string
  recordedAt?: string
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
  participants: {
    personId: string
    displayName?: string
    confirmed: boolean
    completed: boolean
    cancelled: boolean
  }[]
}
type Timeline = {
  recordedStates: {
    recordedAt: string
    mutationVersion: number
    reason: string
    status: 'available' | 'redacted'
    states: State[]
  }[]
  coverage: 'limited'
  reasons: string[]
  window: {
    firstRecordedAt: string | null
    firstMutationVersion: number | null
    prunedThroughMutationVersion: number
    throughMutationVersion: number
  }
}
const opened = ref(false),
  busy = ref(false),
  error = ref(false),
  asOf = ref(''),
  value = ref<Timeline>()
let generation = 0,
  controller: AbortController | undefined,
  disposed = false
function clear() {
  ++generation
  controller?.abort()
  value.value = undefined
  busy.value = false
  error.value = false
}
async function load() {
  clear()
  if (!authenticated.value || disposed) return
  opened.value = true
  const epoch = generation
  controller = new AbortController()
  busy.value = true
  try {
    const query = new URLSearchParams({
      sceneId: props.sceneId,
      version: String(props.anchor.version),
      deletionEpoch: String(props.anchor.deletionEpoch),
      mutationVersion: String(props.anchor.mutationVersion),
      limit: '20',
      ...(asOf.value ? { asOfRecorded: asOf.value } : {}),
    })
    const r = await authenticatedFetch(
      `/api/memory-commitments/${encodeURIComponent(props.anchor.id)}/timeline?${query}`,
      { signal: controller.signal },
    )
    if (!r.ok) throw Error()
    const result = (await r.json()) as Timeline
    if (!disposed && epoch === generation && authenticated.value) value.value = result
  } catch {
    if (epoch === generation) error.value = true
  } finally {
    if (epoch === generation) busy.value = false
  }
}
const date = (s?: string | null) =>
  s && Number.isFinite(Date.parse(s))
    ? new Date(s).toLocaleString(locale.value)
    : t('mpUnknownTime')
watch(
  () => [
    props.sceneId,
    props.anchor.id,
    props.anchor.version,
    props.anchor.deletionEpoch,
    props.anchor.mutationVersion,
    authenticated.value,
  ],
  () => {
    clear()
    opened.value = false
    asOf.value = ''
  },
)
onBeforeUnmount(() => {
  disposed = true
  clear()
})
</script>
<template>
  <section class="space-y-2 rounded border border-default p-3" :aria-label="t('ctTitle')">
    <UButton :disabled="busy || !authenticated" variant="outline" @click="load">{{
      t('ctTitle')
    }}</UButton>
    <template v-if="opened">
      <p class="text-sm text-muted">{{ t('ctHelp') }}</p>
      <label class="block text-sm"
        >{{ t('ctAsOf')
        }}<input
          v-model="asOf"
          type="text"
          placeholder="2026-10-29T10:00:00.000Z"
          :disabled="busy || !authenticated"
          class="block w-full rounded border p-2"
      /></label>
      <UButton :disabled="busy || !authenticated" @click="load">{{ t('ctQuery') }}</UButton>
      <p v-if="busy" role="status">{{ t('uxLoading') }}</p>
      <p v-if="error" role="alert">{{ t('cmHistoryUnavailable') }}</p>
      <template v-if="value">
        <p class="text-sm text-warning">{{ t('ctLimited') }}</p>
        <p class="text-xs">
          {{ t('ctWindow') }}: {{ date(value.window.firstRecordedAt) }} ·
          {{ value.window.firstMutationVersion ?? '—' }}–{{ value.window.throughMutationVersion }} ·
          {{ t('ctPruned') }} {{ value.window.prunedThroughMutationVersion }}
        </p>
        <ul class="text-xs">
          <li v-for="reason in value.reasons" :key="reason">{{ t('ctReason_' + reason) }}</li>
        </ul>
        <p v-if="!value.recordedStates.length">{{ t('ctEmpty') }}</p>
        <article
          v-for="row in value.recordedStates"
          :key="row.mutationVersion"
          class="space-y-2 rounded border p-3"
        >
          <h4 class="font-medium">
            {{ t('ctRecorded') }}: {{ date(row.recordedAt) }} · #{{ row.mutationVersion }}
          </h4>
          <p>{{ t('ctEvent_' + row.reason) }}</p>
          <p v-if="row.status === 'redacted'" class="text-warning">{{ t('ctRedacted') }}</p>
          <div v-for="state in row.states" :key="state.ref.version" class="space-y-1">
            <p>
              {{ t('cmVersion', { version: state.ref.version }) }} · {{ t('ctState') }}:
              {{ t('cmConfirmation_' + state.confirmation) }} /
              {{ t('cmLifecycle_' + state.lifecycle) }}
            </p>
            <h5>{{ state.terms?.title || t('cmTermsUnavailable') }}</h5>
            <template v-if="state.terms">
              <p>
                {{ t('cmPolarity_' + state.terms.polarity) }} ·
                {{ t('mrModality_' + state.terms.modality) }} ·
                {{ t('mrTemporal_' + state.terms.temporalScope) }}
              </p>
              <p>{{ t('mrConditions') }}</p>
              <ul v-if="state.terms.conditions.length" class="list-disc pl-5">
                <li
                  v-for="(condition, index) in state.terms.conditions"
                  :key="index"
                  class="whitespace-pre-wrap"
                >
                  {{ condition }}
                </li>
              </ul>
              <p v-else>{{ t('cmNoConditions') }}</p>
              <p>
                {{ t('cmTimeQualification') }}:
                {{ state.terms.timeQualification || t('mpUnknownTime') }}
              </p>
              <p>
                {{ t('mpOccurred') }}: {{ date(state.terms.occurredAt) }} · {{ t('mpValidFrom') }}:
                {{ date(state.terms.validFrom) }} · {{ t('mpValidUntil') }}:
                {{ date(state.terms.validUntil) }}
              </p>
            </template>
            <ul>
              <li v-for="(person, index) in state.participants" :key="person.personId">
                {{ person.displayName || t('mpUnnamed', { number: index + 1 }) }} ·
                {{ t(person.confirmed ? 'cmPersonConfirmed' : 'cmPersonMissing')
                }}<span v-if="person.completed"> · {{ t('cmPersonCompleted') }}</span
                ><span v-if="person.cancelled"> · {{ t('cmPersonCancelled') }}</span>
              </li>
            </ul>
            <p>{{ t('cmRecorded') }}: {{ date(state.recordedAt) }} · {{ t('cmDeclared') }}</p>
          </div>
        </article>
      </template>
    </template>
  </section>
</template>
