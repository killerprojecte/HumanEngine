<script setup lang="ts">
import type { components } from '~/types/api'
type RecordView = {
  content: string
  kind: string
  epistemicKind?: string
  status: string
  learnedAt: string
  occurredAt?: string
  validFrom?: string
  validUntil?: string
  sources: Array<{ sceneId: string; messageId: string; senderId: string }>
}
type PlanView = {
  sourceMessageIds: string[]
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
    participantIds: string[]
  }
  confirmation: string
  lifecycle: string
  recordedAt?: string
  confirmedParticipantIds: string[]
  missingParticipantIds: string[]
  completedParticipantIds: string[]
  cancelledParticipantIds: string[]
}
type Selection = {
  ref: {
    kind: 'assertion' | 'commitment'
    id: string
    version: number
    deletionEpoch: number
    mutationVersion?: number
  }
  dependencyKey: string
}
type Access = Selection & {
  state: string
  reviewDue: boolean
  pinned: boolean
  pinIntent: boolean
  pinRevision: number
  pinStatus: string
  policyCoverage: string
  contextSelectedCount: number
  lastSelectedAt: number | null
  protection: string | null
}
type Entry = {
  kind: 'assertion' | 'commitment' | 'reported_experience' | 'shared_plan_declaration'
  record?: RecordView
  view?: PlanView
  accessibility?: Access
}
type PinPolicy = {
  selection: Selection
  policyRevision: number
  pinned: boolean
  terminal: boolean
}
const props = defineProps<{
  scenes: components['schemas']['Scene'][]
  sceneId?: string
  personId?: string
}>()
const { t, locale } = useI18n(),
  { authenticated, authenticatedFetch } = useApi()
const selectedScene = ref(''),
  includeCold = ref(false),
  working = ref<Entry[]>([]),
  episodes = ref<Entry[]>([]),
  policies = ref<PinPolicy[]>([]),
  busy = ref(false),
  error = ref(''),
  workingEnabled = ref(false),
  episodesEnabled = ref(false),
  limited = ref(false),
  omittedCold = ref(0)
const scene = computed(() => props.sceneId ?? selectedScene.value),
  scenes = computed(() => props.scenes.filter((s) => ['group', 'private'].includes(s.kind)))
const cards = computed(() => [...working.value, ...episodes.value])
let controller: AbortController | undefined,
  epoch = 0,
  disposed = false
const date = (value?: string | number | null) =>
  value !== undefined && value !== null && Number.isFinite(new Date(value).getTime())
    ? new Date(value).toLocaleString(locale.value)
    : t('mpUnknownTime')
function clear() {
  epoch++
  controller?.abort()
  working.value = []
  episodes.value = []
  policies.value = []
  busy.value = false
  error.value = ''
  limited.value = false
  workingEnabled.value = false
  episodesEnabled.value = false
  omittedCold.value = 0
}
async function load() {
  clear()
  if (disposed || !authenticated.value || !scene.value) return
  const current = epoch
  controller = new AbortController()
  busy.value = true
  try {
    const base = new URLSearchParams({
      sceneId: scene.value,
      ...(props.personId ? { personId: props.personId } : {}),
    })
    const responses = await Promise.all([
      authenticatedFetch(`/api/memory-working-set?${base}&includeCold=${includeCold.value}`, {
        signal: controller.signal,
      }),
      authenticatedFetch(`/api/memory-episodes?${base}`, { signal: controller.signal }),
    ])
    if (responses.some((r) => !r.ok)) throw Error('request')
    const [ws, ep] = await Promise.all(responses.map((r) => r.json()))
    if (disposed || current !== epoch || !authenticated.value) return
    working.value = ws.items
    episodes.value = ep.items
    policies.value = ws.policies?.items ?? []
    workingEnabled.value = ws.enabled
    episodesEnabled.value = ep.enabled
    omittedCold.value = ws.omittedCold
    limited.value =
      ws.coverage === 'limited' ||
      ep.coverage.status === 'limited' ||
      ws.policies?.coverage === 'limited'
  } catch {
    if (current === epoch && authenticated.value) error.value = t('networkError')
  } finally {
    if (current === epoch) busy.value = false
  }
}
async function pin(selection: Selection, expectedRevision: number, pinned: boolean) {
  if (
    busy.value ||
    !authenticated.value ||
    !window.confirm(t(pinned ? 'wwPinConfirm' : 'wwUnpinConfirm'))
  )
    return
  const current = epoch
  controller?.abort()
  controller = new AbortController()
  busy.value = true
  try {
    const response = await authenticatedFetch('/api/memory-working-set/pin', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ sceneId: scene.value, selection, expectedRevision, pinned }),
      signal: controller.signal,
    })
    if (!response.ok) throw Error('request')
    if (current === epoch && authenticated.value) await load()
  } catch {
    if (current === epoch && authenticated.value) error.value = t('wwMutationError')
  } finally {
    if (current === epoch) busy.value = false
  }
}
async function rebuild() {
  if (busy.value || !authenticated.value) return
  const current = epoch
  controller?.abort()
  controller = new AbortController()
  busy.value = true
  try {
    const response = await authenticatedFetch('/api/memory-working-set/rebuild', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        sceneId: scene.value,
        ...(props.personId ? { personId: props.personId } : {}),
      }),
      signal: controller.signal,
    })
    if (!response.ok) throw Error('request')
    if (current === epoch && authenticated.value) await load()
  } catch {
    if (current === epoch && authenticated.value) error.value = t('wwMutationError')
  } finally {
    if (current === epoch) busy.value = false
  }
}
watch([scene, () => props.personId, includeCold, authenticated], () => void load(), {
  immediate: true,
})
watch(scenes, (rows) => {
  if (selectedScene.value && !rows.some((s) => s.id === selectedScene.value))
    selectedScene.value = ''
})
onBeforeUnmount(() => {
  disposed = true
  clear()
})
</script>
<template>
  <section class="rounded border border-default p-4 space-y-3">
    <h2 class="font-semibold">{{ t('wwTitle') }}</h2>
    <details class="text-xs text-muted">
      <summary class="cursor-pointer w-fit">{{ t('mxAboutView') }}</summary>
      <p class="mt-2 max-w-3xl leading-5">{{ t('wwHelp') }}</p>
    </details>
    <div class="flex flex-wrap gap-2">
      <select
        v-if="!props.sceneId"
        v-model="selectedScene"
        :disabled="!authenticated"
        :aria-label="t('mpScene')"
        class="rounded border border-default bg-default p-2"
      >
        <option value="">{{ t('mpChooseScene') }}</option>
        <option v-for="item in scenes" :key="item.id" :value="item.id">
          {{ item.name || t('mpUnnamedScene') }}
        </option></select
      ><UCheckbox
        v-model="includeCold"
        :disabled="!authenticated"
        :label="t('wwIncludeCold')"
      /><UButton variant="outline" :disabled="busy || !scene || !authenticated" @click="load">{{
        t('refresh')
      }}</UButton
      ><UDropdownMenu
        :items="[
          {
            label: t('wwRefreshCandidates'),
            icon: 'i-lucide-list-restart',
            disabled: busy || !scene || !authenticated,
            onSelect: rebuild,
          },
        ]"
      >
        <UButton
          variant="ghost"
          color="neutral"
          icon="i-lucide-ellipsis"
          :aria-label="t('mcActions')"
          :disabled="busy || !scene || !authenticated"
        />
      </UDropdownMenu>
    </div>
    <p v-if="!authenticated">{{ t('mpSignIn') }}</p>
    <p v-if="busy" role="status">{{ t('uxLoading') }}</p>
    <p v-if="error" role="alert" class="text-error">{{ error }}</p>
    <p v-if="scene && !busy && !error && !workingEnabled" class="text-sm text-muted">
      {{ t('wwWorkingDisabled') }}
    </p>
    <p v-if="scene && !busy && !error && !episodesEnabled" class="text-sm text-muted">
      {{ t('wwEpisodesDisabled') }}
    </p>
    <p v-if="scene && !busy && !error && !cards.length">{{ t('wwEmpty') }}</p>
    <p v-if="limited || omittedCold" class="text-sm text-muted">
      {{ t('wwLimited', { count: omittedCold }) }}
    </p>
    <article
      v-for="(entry, index) in cards"
      :key="index"
      class="rounded border border-default p-3 space-y-2"
    >
      <p class="font-medium">{{ t('wwKind_' + entry.kind) }}</p>
      <template v-if="entry.accessibility"
        ><div class="flex flex-wrap gap-2 text-xs">
          <span>{{ t('wwState_' + entry.accessibility.state) }}</span
          ><span v-if="entry.accessibility.reviewDue">{{ t('wwReviewDue') }}</span
          ><span>{{ t('wwPin_' + entry.accessibility.pinStatus) }}</span
          ><span v-if="entry.accessibility.policyCoverage === 'unknown'">{{
            t('wwPolicyUnknown')
          }}</span>
        </div>
        <p class="text-xs text-muted">
          {{ t('wwSelected', { count: entry.accessibility.contextSelectedCount }) }} ·
          {{ date(entry.accessibility.lastSelectedAt) }}
        </p>
        <p v-if="entry.accessibility.protection" class="text-xs text-muted">
          {{ t('wwProtected') }}
        </p>
        <UButton
          variant="outline"
          :disabled="busy || !authenticated || entry.accessibility.pinStatus === 'terminal'"
          @click="
            pin(
              { ref: entry.accessibility.ref, dependencyKey: entry.accessibility.dependencyKey },
              entry.accessibility.pinRevision,
              !entry.accessibility.pinIntent,
            )
          "
          >{{ t(entry.accessibility.pinIntent ? 'wwUnpin' : 'wwPin') }}</UButton
        ></template
      >
      <template v-if="entry.record"
        ><p class="whitespace-pre-wrap break-words">{{ entry.record.content }}</p>
        <p class="text-xs">
          {{ t('mpEpistemic_' + (entry.record.epistemicKind || 'unknown')) }} ·
          {{ t('memoryStatus_' + entry.record.status) }}
        </p>
        <dl class="grid sm:grid-cols-2 gap-2 text-xs">
          <div>
            <dt>{{ t('mpOccurred') }}</dt>
            <dd>{{ date(entry.record.occurredAt) }}</dd>
          </div>
          <div>
            <dt>{{ t('mpLearnedTime') }}</dt>
            <dd>{{ date(entry.record.learnedAt) }}</dd>
          </div>
          <div>
            <dt>{{ t('mpValidFrom') }}</dt>
            <dd>{{ date(entry.record.validFrom) }}</dd>
          </div>
          <div>
            <dt>{{ t('mpValidUntil') }}</dt>
            <dd>{{ date(entry.record.validUntil) }}</dd>
          </div>
        </dl>
        <details class="text-xs">
          <summary>{{ t('mpSourceDetails') }}</summary>
          <p>{{ t('cmSourceUnavailable') }}</p>
          <p v-for="source in entry.record.sources" :key="source.messageId" class="break-all">
            {{ source.messageId }}
          </p>
        </details></template
      >
      <template v-if="entry.view"
        ><p class="font-medium">{{ entry.view.terms?.title || t('cmTermsUnavailable') }}</p>
        <p class="text-xs">
          {{ t('cmConfirmation_' + entry.view.confirmation) }} ·
          {{ t('cmLifecycle_' + entry.view.lifecycle) }}
        </p>
        <template v-if="entry.view.terms"
          ><p>
            {{ t('cmPolarity_' + entry.view.terms.polarity) }} ·
            {{ t('mrModality_' + entry.view.terms.modality) }} ·
            {{ t('mrTemporal_' + entry.view.terms.temporalScope) }}
          </p>
          <ul class="list-disc pl-5">
            <li
              v-for="(condition, n) in entry.view.terms.conditions"
              :key="n"
              class="whitespace-pre-wrap"
            >
              {{ condition }}
            </li>
          </ul>
          <p v-if="!entry.view.terms.conditions.length">{{ t('cmNoConditions') }}</p>
          <p>
            {{ t('cmTimeQualification') }}:
            {{ entry.view.terms.timeQualification || t('mpUnknownTime') }}
          </p>
          <p class="text-xs">
            {{ t('mpOccurred') }}: {{ date(entry.view.terms.occurredAt) }} · {{ t('mpValidFrom') }}:
            {{ date(entry.view.terms.validFrom) }} · {{ t('mpValidUntil') }}:
            {{ date(entry.view.terms.validUntil) }}
          </p>
          <details class="text-xs">
            <summary>{{ t('wwParticipantDeclarations') }}</summary>
            <p>{{ t('cmSourceUnavailable') }}</p>
            <p v-for="sourceId in entry.view.sourceMessageIds" :key="sourceId" class="break-all">
              {{ sourceId }}
            </p>
            <p v-for="(person, n) in entry.view.terms.participantIds" :key="person">
              {{ t('mpUnnamed', { number: n + 1 }) }} ·
              {{
                t(
                  entry.view.confirmedParticipantIds.includes(person)
                    ? 'cmPersonConfirmed'
                    : 'cmPersonMissing',
                )
              }}<span v-if="entry.view.completedParticipantIds.includes(person)">
                · {{ t('cmPersonCompleted') }}</span
              ><span v-if="entry.view.cancelledParticipantIds.includes(person)">
                · {{ t('cmPersonCancelled') }}</span
              >
            </p>
          </details></template
        >
        <p class="text-xs">{{ t('cmRecorded') }}: {{ date(entry.view.recordedAt) }}</p>
        <p class="text-xs text-muted">{{ t('wwPlanDeclaration') }}</p></template
      >
    </article>
    <section v-if="policies.length" class="border-t border-default pt-3 space-y-2">
      <h3>{{ t('wwStoredPins') }}</h3>
      <p class="text-xs text-muted">{{ t('wwStoredPinsHelp') }}</p>
      <div
        v-for="policy in policies"
        :key="policy.selection.ref.kind + policy.selection.ref.id"
        class="text-sm"
      >
        <span>{{
          t(
            policy.terminal
              ? 'wwPinTerminal'
              : policy.pinned
                ? 'wwPinIntentUnchecked'
                : 'wwPinReleased',
          )
        }}</span>
        <details class="text-xs">
          <summary>{{ t('wwEntityReference') }}</summary>
          <p class="break-all">{{ policy.selection.ref.id }}</p>
        </details>
        <UButton
          v-if="policy.pinned && !policy.terminal"
          variant="outline"
          :disabled="busy || !authenticated"
          @click="pin(policy.selection, policy.policyRevision, false)"
          >{{ t('wwUnpin') }}</UButton
        >
      </div>
    </section>
  </section>
</template>
