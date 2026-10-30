<script setup lang="ts">
import type { components } from '~/types/api'
type Source = { sceneId: string; messageId: string; senderId: string }
type Ref = { id: string; version: number; deletionEpoch: number }
type Entry = {
  id: string
  version: number
  kind: string
  text: string
  status: string
  admission: string
  hypothesis: boolean
  createdAt: string
  applicableUntil: string | null
  applicability: string
  interpretation: string
  rootCount: number
  episodeAnchor?: { id: string; version: number }
  anchorQualifications: {
    edgeId: string
    predicate: string
    property?: string
    status: string
    admission: string
    polarity: string
    modality: string
    temporalScope: string
    conditions: string[]
    occurredAt: string | null
    validFrom: string | null
    validUntil: string | null
  }[]
  parents: {
    ref: Ref
    proofs: { sources: Source[]; parents: string[]; parentReferences?: Ref[] }[]
  }[]
  declarations: {
    ref: Ref
    record: {
      content: string
      kind: string
      epistemicKind?: string
      status: string
      scope: { kind: string; sceneId?: string; personId?: string }
      origin: string
      observedAt?: string
      learnedAt: string
      occurredAt?: string
      validFrom?: string
      validUntil?: string
      internalUse?: string
      sources: Source[]
    }
  }[]
}
const props = defineProps<{ scenes: components['schemas']['Scene'][] }>()
const { t, locale } = useI18n(),
  { authenticated, authenticatedFetch } = useApi()
const sceneId = ref(''),
  busy = ref(false),
  error = ref(false),
  page = ref<{
    enabled: boolean
    coverage: string
    items: Entry[]
    unavailable: { id: string; version: number; status: string; reason: string }[]
  }>()
const scenes = computed(() => props.scenes.filter((s) => ['group', 'private'].includes(s.kind)))
let controller: AbortController | undefined,
  epoch = 0
const date = (value?: string | null) =>
  value ? new Date(value).toLocaleString(locale.value) : t('knUnknown')
function clear() {
  epoch++
  controller?.abort()
  page.value = undefined
  error.value = false
  busy.value = false
}
async function load() {
  clear()
  if (!authenticated.value || !sceneId.value) return
  const mine = epoch
  controller = new AbortController()
  busy.value = true
  try {
    const response = await authenticatedFetch(
      new URL(
        '/api/memory-semantics?' + new URLSearchParams({ sceneId: sceneId.value }),
        location.origin,
      ),
      { signal: controller.signal },
    )
    if (!response.ok) throw new Error('read')
    const value = await response.json()
    if (mine === epoch) page.value = value
  } catch {
    if (mine === epoch) error.value = true
  } finally {
    if (mine === epoch) busy.value = false
  }
}
async function review(entry: Entry, status: 'admitted' | 'rejected') {
  if (busy.value || error.value || !authenticated.value || !window.confirm(t('smConfirm'))) return
  const mine = epoch
  busy.value = true
  try {
    const response = await authenticatedFetch(
      new URL('/api/memory-semantics/' + encodeURIComponent(entry.id) + '/review', location.origin),
      {
        method: 'POST',
        signal: controller?.signal,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          sceneId: sceneId.value,
          expectedVersion: entry.version,
          status,
          confirmed: true,
        }),
      },
    )
    if (!response.ok) throw new Error('review')
    if (mine === epoch) await load()
  } catch {
    if (mine === epoch) error.value = true
  } finally {
    if (mine === epoch) busy.value = false
  }
}
watch([sceneId, authenticated], load)
onBeforeUnmount(clear)
</script>
<template>
  <section class="mt-6 space-y-3 rounded-lg border border-default p-4">
    <h3 class="font-semibold">{{ t('smTitle') }}</h3>
    <details class="text-xs text-muted">
      <summary class="cursor-pointer w-fit">{{ t('mxAboutView') }}</summary>
      <p class="mt-2 max-w-3xl leading-5">{{ t('smHelp') }}</p>
    </details>
    <label
      >{{ t('knScene')
      }}<select v-model="sceneId" class="ml-2 rounded border border-default bg-default p-2">
        <option value="">—</option>
        <option v-for="scene in scenes" :key="scene.id" :value="scene.id">{{ scene.name }}</option>
      </select></label
    ><UButton :disabled="busy || !sceneId" variant="ghost" @click="load">{{
      t('ilRefresh')
    }}</UButton>
    <p v-if="busy" role="status">{{ t('knLoading') }}</p>
    <p v-if="error" role="alert">{{ t('smError') }}</p>
    <template v-if="page"
      ><p v-if="!page.enabled">{{ t('smDisabled') }}</p>
      <p v-if="page.coverage === 'limited'">{{ t('smLimited') }}</p>
      <p v-if="!page.items.length && !page.unavailable.length">{{ t('knEmpty') }}</p>
      <article
        v-for="entry in page.unavailable"
        :key="'unavailable-' + entry.id"
        class="rounded border border-default p-3"
      >
        <p class="break-all">
          {{ entry.id }} / v{{ entry.version }} · {{ t('smStatus_' + entry.status) }}
        </p>
        <p>{{ t('smUnavailable') }}</p>
      </article>
      <article
        v-for="entry in page.items"
        :key="entry.id"
        class="space-y-2 rounded border border-default p-3"
      >
        <h4 class="font-medium">
          {{ t('smKind_' + entry.kind) }} · {{ t('smStatus_' + entry.status) }}
        </h4>
        <p>{{ t('smInterpretation_' + entry.interpretation) }}</p>
        <p class="whitespace-pre-wrap">{{ entry.text }}</p>
        <p>
          {{ t('smApplicability_' + entry.applicability) }} ·
          {{ t('smAdmission_' + entry.admission) }}
        </p>
        <p v-if="entry.hypothesis">{{ t('smHypothesis') }}</p>
        <p>
          {{ t('knCreated') }}: {{ date(entry.createdAt) }} · {{ t('smUntil') }}:
          {{ date(entry.applicableUntil) }}
        </p>
        <p>{{ t('knVersion') }}: {{ entry.version }} · {{ t('smRoots') }}: {{ entry.rootCount }}</p>
        <p v-if="entry.episodeAnchor">
          {{ t('smAnchor') }}: {{ entry.episodeAnchor.id }} / v{{ entry.episodeAnchor.version }}
        </p>
        <section
          v-if="entry.anchorQualifications?.length"
          class="space-y-2 rounded border border-default p-3"
        >
          <h5>{{ t('smAnchorQualifications') }}</h5>
          <p class="text-sm text-muted">{{ t('smAnchorQualificationHelp') }}</p>
          <article
            v-for="q in entry.anchorQualifications"
            :key="q.edgeId"
            class="border-l border-default pl-3"
          >
            <p>
              {{ t('knPredicate_' + q.predicate) }} · {{ t('knStatus_' + q.status) }} ·
              {{ t('knAdmission_' + q.admission) }}
            </p>
            <p>
              {{ t('knPolarity_' + q.polarity) }} · {{ t('knModality_' + q.modality) }} ·
              {{ t('knTemporal_' + q.temporalScope) }}
            </p>
            <p>
              {{ t('knConditions') }}:
              {{ q.conditions.length ? q.conditions.join('；') : t('knNoConditions') }}
            </p>
            <p>
              {{ t('knOccurrence') }}: {{ date(q.occurredAt) }} · {{ t('knValidity') }}:
              {{ date(q.validFrom) }} → {{ date(q.validUntil) }}
            </p>
          </article>
        </section>
        <div class="flex gap-2">
          <UButton
            :disabled="busy || error || entry.status === 'admitted'"
            @click="review(entry, 'admitted')"
            >{{ t('smAdmit') }}</UButton
          ><UButton
            :disabled="busy || error || entry.status === 'rejected'"
            color="warning"
            variant="ghost"
            @click="review(entry, 'rejected')"
            >{{ t('smReject') }}</UButton
          >
        </div>
        <details>
          <summary>{{ t('smDeclarations') }}</summary>
          <p class="text-sm text-muted">{{ t('smProofHelp') }}</p>
          <article
            v-for="declaration in entry.declarations"
            :key="declaration.ref.id"
            class="my-3 border-l border-default pl-3"
          >
            <p class="break-all">
              {{ declaration.ref.id }} / v{{ declaration.ref.version }} /
              {{ declaration.ref.deletionEpoch }}
            </p>
            <p class="whitespace-pre-wrap">
              {{
                declaration.record.internalUse
                  ? t('knInternalCredential')
                  : declaration.record.content
              }}
            </p>
            <p>
              {{ t('memoryKind_' + declaration.record.kind) }} ·
              {{
                t(
                  declaration.record.epistemicKind
                    ? 'mpEpistemic_' + declaration.record.epistemicKind
                    : 'mpEpistemic_unknown',
                )
              }}
              · {{ t('knBacking_' + declaration.record.status) }} ·
              {{ t('mpOrigin_' + declaration.record.origin) }}
            </p>
            <p class="break-all">
              {{ t('mpSubjectLabel') }}: {{ declaration.record.scope.personId || t('knUnknown') }}
            </p>
            <p>{{ t('mpSourceReceivedTime') }}: {{ date(declaration.record.observedAt) }}</p>
            <p>
              {{ t('knOccurrence') }}: {{ date(declaration.record.occurredAt) }} ·
              {{ t('knLearned') }}: {{ date(declaration.record.learnedAt) }}
            </p>
            <p>
              {{ t('knValidity') }}: {{ date(declaration.record.validFrom) }} →
              {{ date(declaration.record.validUntil) }}
            </p>
            <div
              v-for="(proof, index) in entry.parents.find((p) => p.ref.id === declaration.ref.id)
                ?.proofs || []"
              :key="index"
            >
              <strong>{{ t('knGroup') }} {{ index + 1 }}</strong>
              <ul>
                <li v-for="source in proof.sources" :key="source.messageId" class="break-all">
                  {{ source.sceneId }} / {{ source.messageId }} / {{ source.senderId }}
                </li>
                <li v-for="parent in proof.parentReferences || []" :key="parent.id">
                  {{ parent.id }} / v{{ parent.version }} / {{ parent.deletionEpoch }}
                </li>
                <li v-if="proof.parents.length && !proof.parentReferences">
                  {{ t('knUnknown') }}: {{ proof.parents.join(', ') }}
                </li>
              </ul>
            </div>
          </article>
        </details>
      </article></template
    >
  </section>
</template>
