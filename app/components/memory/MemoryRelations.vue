<script setup lang="ts">
import type { components } from '~/types/api'
type Scene = components['schemas']['Scene']
type Relation = {
  assertionContent: string
  id: string
  version: number
  personId: string
  personName?: string
  predicate: string
  target: { id: string; kind: string; title: string }
  modality: string
  temporalScope: string
  polarity: string
  conditions: string[]
  occurredAt?: string
  validFrom?: string
  validUntil?: string
  epistemicKind?: string
  status: string
  admission: string
  autoReason?: string
  createdAt: string
  reviewedAt?: string
  sourceGroups: Array<{
    witnessKind?: string
    sources: Array<{ messageId: string; senderId: string; senderName?: string }>
    parentCount: number
  }>
}
const props = defineProps<{ scenes: Scene[] }>()
const { t, locale } = useI18n()
const { authenticatedFetch, authenticated } = useApi()
const sceneId = ref(''),
  filter = ref(''),
  items = ref<Relation[]>([]),
  coverage = ref('complete'),
  enabled = ref(false),
  busy = ref(false),
  error = ref('')
let controller: AbortController | undefined,
  epoch = 0,
  disposed = false
const personFilter = ref('')
const people = computed(() => [
  ...new Map(
    items.value.map((row) => [row.personId, { id: row.personId, name: row.personName }]),
  ).values(),
])
const visibleItems = computed(() =>
  items.value.filter((row) => !personFilter.value || row.personId === personFilter.value),
)
const realScenes = computed(() =>
  props.scenes.filter((scene) => ['group', 'private'].includes(scene.kind)),
)
const date = (value?: string) =>
  value && Number.isFinite(Date.parse(value))
    ? new Date(value).toLocaleString(locale.value)
    : t('mpUnknownTime')
async function load() {
  const current = ++epoch
  controller?.abort()
  controller = new AbortController()
  items.value = []
  personFilter.value = ''
  error.value = ''
  enabled.value = false
  busy.value = false
  if (disposed || !authenticated.value || !sceneId.value) return
  busy.value = true
  try {
    const response = await authenticatedFetch(
      `/api/memory-relations?${new URLSearchParams({ sceneId: sceneId.value, ...(filter.value ? { status: filter.value } : {}) })}`,
      { signal: controller.signal },
    )
    if (!response.ok) throw new Error('load')
    const value = await response.json()
    if (disposed || current !== epoch || !authenticated.value) return
    items.value = value.items
    coverage.value = value.coverage
    enabled.value = value.enabled
  } catch {
    if (current === epoch && authenticated.value) error.value = t('networkError')
  } finally {
    if (current === epoch) busy.value = false
  }
}
async function review(row: Relation, status: 'accepted' | 'rejected') {
  if (
    busy.value ||
    !authenticated.value ||
    !window.confirm(t(status === 'accepted' ? 'mrAcceptConfirm' : 'mrRejectConfirm'))
  )
    return
  const current = epoch,
    scene = sceneId.value
  busy.value = true
  error.value = ''
  controller?.abort()
  controller = new AbortController()
  try {
    const response = await authenticatedFetch(
      `/api/memory-relations/${encodeURIComponent(row.id)}/review`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sceneId: scene, expectedVersion: row.version, status }),
        signal: controller.signal,
      },
    )
    if (!response.ok) throw new Error(response.status === 409 ? 'conflict' : 'request')
    if (current === epoch && authenticated.value) await load()
  } catch (cause) {
    if (current === epoch && authenticated.value)
      error.value = t(
        cause instanceof Error && cause.message === 'conflict' ? 'mrConflict' : 'mrUncertain',
      )
  } finally {
    if (current === epoch) busy.value = false
  }
}
watch([sceneId, filter, authenticated], () => void load())
watch(realScenes, (rows) => {
  if (sceneId.value && !rows.some((row) => row.id === sceneId.value)) sceneId.value = ''
})
onBeforeUnmount(() => {
  disposed = true
  epoch++
  controller?.abort()
})
</script>
<template>
  <section class="rounded-lg border border-default p-4 space-y-3">
    <h2 class="font-semibold">{{ t('mrTitle') }}</h2>
    <p class="text-sm text-muted">{{ t('mrHelp') }}</p>
    <div class="flex flex-wrap gap-2">
      <select
        v-model="sceneId"
        :disabled="!authenticated"
        :aria-label="t('mpScene')"
        class="rounded border border-default bg-default p-2"
      >
        <option value="">{{ t('mpChooseScene') }}</option>
        <option v-for="scene in realScenes" :key="scene.id" :value="scene.id">
          {{ scene.name || t('mpUnnamedScene') }}
        </option>
      </select>
      <select
        v-model="filter"
        :disabled="!authenticated"
        :aria-label="t('mrStatusFilter')"
        class="rounded border border-default bg-default p-2"
      >
        <option value="">{{ t('mrAll') }}</option>
        <option
          v-for="status in ['candidate', 'accepted', 'rejected']"
          :key="status"
          :value="status"
        >
          {{ t('mrStatus_' + status) }}
        </option>
      </select>
      <UButton variant="outline" :disabled="!sceneId || !authenticated || busy" @click="load">{{
        t('refresh')
      }}</UButton>
    </div>
    <label v-if="people.length" class="block text-sm"
      >{{ t('mrPersonFilter') }}
      <select v-model="personFilter" class="ml-2 rounded border border-default bg-default p-2">
        <option value="">{{ t('mrAllPeople') }}</option>
        <option v-for="(person, index) in people" :key="person.id" :value="person.id">
          {{ person.name || t('mpUnnamed', { number: index + 1 }) }}
        </option>
      </select>
    </label>
    <p v-if="!authenticated">{{ t('mpSignIn') }}</p>
    <p v-else-if="busy" role="status">{{ t('uxLoading') }}</p>
    <p v-if="error" role="alert" class="text-error">{{ error }}</p>
    <p v-if="sceneId && !busy && !enabled && !error" class="text-sm text-muted">
      {{ t('mrDisabled') }}
    </p>
    <p v-else-if="sceneId && !busy && !items.length && !error" class="text-muted">
      {{ t('mrEmpty') }}
    </p>
    <p v-if="coverage === 'limited'" class="text-sm text-muted">{{ t('mrLimited') }}</p>
    <article
      v-for="row in visibleItems"
      :key="row.id"
      class="rounded border border-default p-3 space-y-2"
    >
      <div class="text-xs text-muted flex flex-wrap gap-2">
        <span>{{ t('mrStatus_' + row.status) }}</span
        ><span>{{ t('mrAdmission_' + row.admission) }}</span
        ><span>{{ t('mpEpistemic_' + (row.epistemicKind || 'unknown')) }}</span>
      </div>
      <p class="font-medium">
        {{ row.personName || t('mpUnknownPerson') }} · {{ t('mrPredicate') }} ·
        {{ row.target.title }}
      </p>
      <p>{{ t('mrPolarity_' + row.polarity) }}</p>
      <p class="text-sm">
        {{ t('mrModality_' + row.modality) }} · {{ t('mrTemporal_' + row.temporalScope) }}
      </p>
      <p class="text-sm whitespace-pre-wrap break-words">{{ row.assertionContent }}</p>
      <div>
        <span class="text-sm text-muted">{{ t('mrConditions') }}</span>
        <ul v-if="row.conditions.length" class="list-disc pl-5">
          <li
            v-for="(condition, index) in row.conditions"
            :key="index"
            class="whitespace-pre-wrap break-words"
          >
            {{ condition }}
          </li>
        </ul>
        <p v-else class="text-sm">{{ t('mrNoConditions') }}</p>
      </div>
      <p v-if="row.autoReason" class="text-xs text-muted">{{ t('mrNoVisibleConflict') }}</p>
      <dl class="grid sm:grid-cols-3 gap-2 text-xs">
        <div>
          <dt>{{ t('mpOccurred') }}</dt>
          <dd>{{ date(row.occurredAt) }}</dd>
        </div>
        <div>
          <dt>{{ t('mpValidFrom') }}</dt>
          <dd>{{ date(row.validFrom) }}</dd>
        </div>
        <div>
          <dt>{{ t('mpValidUntil') }}</dt>
          <dd>{{ date(row.validUntil) }}</dd>
        </div>
        <div>
          <dt>{{ t('mrCreated') }}</dt>
          <dd>{{ date(row.createdAt) }}</dd>
        </div>
        <div>
          <dt>{{ t('mrReviewed') }}</dt>
          <dd>{{ date(row.reviewedAt) }}</dd>
        </div>
      </dl>
      <details class="text-xs text-muted">
        <summary>{{ t('mpSourceDetails') }}</summary>
        <p>{{ t('mrProofHelp') }}</p>
        <div v-for="(group, index) in row.sourceGroups" :key="index" class="mt-2 border-l pl-2">
          <p>{{ t('mrProofGroup', { number: index + 1, parents: group.parentCount }) }}</p>
          <p v-if="group.witnessKind">{{ t('mpWitness_' + group.witnessKind) }}</p>
          <p v-for="source in group.sources" :key="source.messageId" class="break-all">
            {{ source.senderName || t('mpUnknownPerson') }} · {{ source.messageId }}
          </p>
        </div>
        <p class="break-all">{{ t('mpIdentity') }}: {{ row.personId }}</p>
        <p class="break-all">{{ t('mrEntityIdentity') }}: {{ row.target.id }}</p>
      </details>
      <div class="flex gap-2">
        <UButton
          v-if="row.status !== 'accepted'"
          :disabled="busy || !authenticated"
          @click="review(row, 'accepted')"
          >{{ t('mrAccept') }}</UButton
        ><UButton
          v-if="row.status !== 'rejected'"
          variant="outline"
          :disabled="busy || !authenticated"
          @click="review(row, 'rejected')"
          >{{ t('mrReject') }}</UButton
        >
      </div>
    </article>
  </section>
</template>
