<script setup lang="ts">
import type { components } from '~/types/api'
type Ref = { id: string; version: number; deletionEpoch: number }
type Endpoint =
  | { kind: 'person'; personId: string }
  | { kind: 'entity'; entity: { id: string; title: string; backing: Ref } }
  | { kind: 'literal'; value: string | number | boolean; unit?: string | null }
type Proof = {
  sources: { sceneId: string; messageId: string; senderId: string }[]
  parents: string[]
  parentReferences?: Ref[]
}
type Edge = {
  id: string
  version: number
  predicate: string
  property?: string
  subject: Endpoint
  object: Endpoint
  polarity: string
  modality: string
  temporalScope: string
  conditions: string[]
  validFrom?: string
  validUntil?: string
  createdAt: string
  status: string
  admission: string
  backingStatus: string
  applicability: string
  backing: Ref
  proofs: Proof[]
  requiresPersonalization: boolean
  readDependencies: { backing: Ref; proofs: Proof[]; edgeId?: string; edgeVersion?: number }[]
  dependencies: { entity: { id: string; title: string }; proofs: Proof[] }[]
  spans: {
    source: { messageId: string; senderId: string }
    partIndex: number
    start: number
    end: number
    stance: string
  }[]
}
type ClosureRecord = {
  ref: Ref
  record: {
    id: string
    content: string
    kind: string
    epistemicKind?: string
    status: string
    origin: string
    learnedAt: string
    observedAt?: string
    occurredAt?: string
    validFrom?: string
    validUntil?: string
    internalUse?: string
    sources: { sceneId: string; messageId: string; senderId: string }[]
  }
}
type Result = {
  supportClosure: { edges: Edge[]; records: ClosureRecord[] }
  enabled: boolean
  edges: Edge[]
  paths: string[][]
  coverage: 'complete' | 'limited'
  reason?: string
}
const props = defineProps<{ scenes: components['schemas']['Scene'][] }>()
const { t, locale } = useI18n(),
  { authenticated, authenticatedFetch } = useApi()
const sceneId = ref(''),
  personId = ref(''),
  depth = ref<1 | 2>(1),
  busy = ref(false),
  error = ref(false),
  result = ref<Result>()
const anchorKind = ref<'person' | 'memory'>('person'),
  memoryId = ref(''),
  memoryVersion = ref<number | undefined>(),
  memoryEpoch = ref<number | undefined>()
const validAnchor = computed(() =>
  anchorKind.value === 'person'
    ? !!personId.value.trim()
    : !!memoryId.value.trim() &&
      typeof memoryVersion.value === 'number' &&
      typeof memoryEpoch.value === 'number' &&
      Number.isSafeInteger(memoryVersion.value) &&
      memoryVersion.value > 0 &&
      Number.isSafeInteger(memoryEpoch.value) &&
      memoryEpoch.value >= 0,
)
const displayEdges = computed(() =>
  result.value ? [...result.value.edges, ...result.value.supportClosure.edges] : [],
)
const scenes = computed(() => props.scenes.filter((s) => ['group', 'private'].includes(s.kind)))
const predicates = [
  'participates_in',
  'prefers_topic',
  'uses_tool',
  'member_of',
  'part_of',
  'depends_on',
  'has_property',
]
let controller: AbortController | undefined,
  epoch = 0
function clear() {
  epoch++
  controller?.abort()
  result.value = undefined
  busy.value = false
  error.value = false
}
const date = (v?: string) => (v ? new Date(v).toLocaleString(locale.value) : t('knUnknown'))
const label = (p: Endpoint) =>
  p.kind === 'person'
    ? p.personId
    : p.kind === 'entity'
      ? p.entity.title
      : `${p.value}${p.unit ? ' ' + p.unit : ''}`
async function query(point?: Endpoint) {
  clear()
  if (!authenticated.value || !sceneId.value || !validAnchor.value) return
  const mine = epoch
  controller = new AbortController()
  busy.value = true
  try {
    const anchor =
      point?.kind === 'entity'
        ? { kind: 'entity', entityId: point.entity.id, backingRef: point.entity.backing }
        : anchorKind.value === 'memory'
          ? {
              kind: 'memory',
              backingRef: {
                id: memoryId.value.trim(),
                version: memoryVersion.value,
                deletionEpoch: memoryEpoch.value,
              },
            }
          : { kind: 'person', personId: personId.value.trim() }
    const response = await authenticatedFetch(
      new URL('/api/memory-knowledge/query', location.origin),
      {
        method: 'POST',
        signal: controller.signal,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          sceneId: sceneId.value,
          ...(anchorKind.value === 'person'
            ? { personId: personId.value.trim() }
            : {
                seedBackingRef: {
                  id: memoryId.value.trim(),
                  version: memoryVersion.value,
                  deletionEpoch: memoryEpoch.value,
                },
              }),
          anchor,
          predicates,
          depth: depth.value,
        }),
      },
    )
    if (!response.ok) throw new Error('query')
    const value = (await response.json()) as Result
    if (mine === epoch) result.value = value
  } catch {
    if (mine === epoch) error.value = true
  } finally {
    if (mine === epoch) busy.value = false
  }
}
watch(
  [sceneId, personId, depth, authenticated, anchorKind, memoryId, memoryVersion, memoryEpoch],
  clear,
)
watch(authenticated, (value) => {
  if (!value) {
    memoryId.value = ''
    memoryVersion.value = undefined
    memoryEpoch.value = undefined
    personId.value = ''
    sceneId.value = ''
  }
})
onBeforeUnmount(clear)
</script>
<template>
  <section class="mt-6 space-y-4 rounded-lg border border-default p-4">
    <h3 class="font-semibold">{{ t('knTitle') }}</h3>
    <p class="text-sm text-muted">{{ t('knHelp') }}</p>
    <p class="text-sm text-muted">{{ t('knExactSeedHelp') }}</p>
    <div class="flex flex-wrap items-end gap-3">
      <label
        >{{ t('knScene')
        }}<select v-model="sceneId" class="block rounded border border-default bg-default p-2">
          <option value="">—</option>
          <option v-for="s in scenes" :key="s.id" :value="s.id">{{ s.name }}</option>
        </select></label
      >
      <label
        >{{ t('knAnchorKind')
        }}<select v-model="anchorKind" class="block rounded border border-default bg-default p-2">
          <option value="person">{{ t('knPersonSeed') }}</option>
          <option value="memory">{{ t('knMemorySeed') }}</option>
        </select></label
      >
      <label v-if="anchorKind === 'person'"
        >{{ t('knPerson') }}<UInput v-model="personId" class="block"
      /></label>
      <template v-else
        ><label>{{ t('knMemoryId') }}<UInput v-model="memoryId" class="block" /></label
        ><label
          >{{ t('knVersion')
          }}<input
            v-model.number="memoryVersion"
            type="number"
            min="1"
            step="1"
            class="block w-24 border border-default bg-default" /></label
        ><label
          >{{ t('knDeletionEpoch')
          }}<input
            v-model.number="memoryEpoch"
            type="number"
            min="0"
            step="1"
            class="block w-24 border border-default bg-default" /></label
      ></template>
      <label
        >{{ t('knDepth')
        }}<select v-model="depth" class="block rounded border border-default bg-default p-2">
          <option :value="1">1</option>
          <option :value="2">2</option>
        </select></label
      >
      <UButton :disabled="busy || !sceneId || !validAnchor" @click="query()">{{
        t('knQuery')
      }}</UButton>
    </div>
    <p v-if="busy" role="status">{{ t('knLoading') }}</p>
    <p v-if="error" role="alert">{{ t('knError') }}</p>
    <template v-if="result">
      <p v-if="!result.enabled">{{ t('knDisabled') }}</p>
      <p v-if="result.coverage === 'limited'" role="status">{{ t('knLimited') }}</p>
      <p v-else-if="!result.edges.length">{{ t('knEmpty') }}</p>
      <article
        v-for="edge in displayEdges"
        :key="edge.id"
        class="space-y-2 rounded-lg border border-default p-3"
      >
        <p
          v-if="result.supportClosure.edges.some((item) => item.id === edge.id)"
          class="text-sm text-muted"
        >
          {{ t('knInheritedEdge') }}
        </p>
        <p v-if="edge.requiresPersonalization" class="text-sm">{{ t('knPersonalized') }}</p>
        <p class="font-medium">
          {{ label(edge.subject) }} · {{ t('knPredicate_' + edge.predicate) }}
          <span v-if="edge.property">({{ t('knProperty_' + edge.property) }})</span> ·
          {{ label(edge.object) }}
        </p>
        <p>
          {{ t('knStatus_' + edge.status) }} · {{ t('knPolarity_' + edge.polarity) }} ·
          {{ t('knModality_' + edge.modality) }} · {{ t('knTemporal_' + edge.temporalScope) }}
        </p>
        <p>
          {{ t('knConditions') }}:
          {{ edge.conditions.length ? edge.conditions.join('；') : t('knNoConditions') }}
        </p>
        <p>{{ t('knValidity') }}: {{ date(edge.validFrom) }} → {{ date(edge.validUntil) }}</p>
        <p>
          {{ t('knCreated') }}: {{ date(edge.createdAt) }} · {{ t('knVersion') }}:
          {{ edge.version }}
        </p>
        <p class="text-sm text-muted">{{ t('knAdmission_' + edge.admission) }}</p>
        <p class="text-sm">
          {{ t('knBacking') }}: {{ edge.backing.id }} / v{{ edge.backing.version }} /
          {{ edge.backing.deletionEpoch }} · {{ t('knBacking_' + edge.backingStatus) }}
        </p>
        <p>{{ t('knApplicability_' + edge.applicability) }}</p>
        <UButton
          v-for="(point, index) in [edge.subject, edge.object].filter((p) => p.kind === 'entity')"
          :key="index"
          variant="ghost"
          :disabled="busy"
          @click="query(point)"
          >{{ t('knExpand') }}: {{ label(point) }}</UButton
        >
        <details>
          <summary>{{ t('knEvidence') }}</summary>
          <p class="text-sm text-muted">{{ t('knProofHelp') }}</p>
          <div
            v-for="(proof, index) in edge.proofs"
            :key="index"
            class="my-2 border-l border-default pl-3"
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
          <div
            v-for="(dependency, index) in edge.readDependencies"
            :key="'read-' + index"
            class="my-2 rounded border border-default p-2"
          >
            <strong>{{ t('knInheritedGroups') }}</strong>
            <p>
              {{ dependency.backing.id }} / v{{ dependency.backing.version }} /
              {{ dependency.backing.deletionEpoch }}
            </p>
            <p v-if="dependency.edgeId">{{ dependency.edgeId }} / v{{ dependency.edgeVersion }}</p>
            <div v-for="(group, groupIndex) in dependency.proofs" :key="groupIndex">
              <p>{{ t('knGroup') }} {{ groupIndex + 1 }}</p>
              <ul>
                <li v-for="source in group.sources" :key="source.messageId" class="break-all">
                  {{ source.sceneId }} / {{ source.messageId }} / {{ source.senderId }}
                </li>
                <li v-for="parent in group.parentReferences || []" :key="parent.id">
                  {{ parent.id }} / v{{ parent.version }} / {{ parent.deletionEpoch }}
                </li>
                <li v-if="group.parents.length && !group.parentReferences">
                  {{ t('knUnknown') }}: {{ group.parents.join(', ') }}
                </li>
              </ul>
            </div>
          </div>
          <div v-for="dependency in edge.dependencies" :key="dependency.entity.id">
            <strong>{{ t('knDependency') }}: {{ dependency.entity.title }}</strong>
            <pre class="overflow-auto whitespace-pre-wrap text-xs">{{
              JSON.stringify(dependency.proofs, null, 2)
            }}</pre>
          </div>
          <ul>
            <li v-for="(span, index) in edge.spans" :key="index" class="break-all">
              {{ span.source.messageId }} / {{ span.source.senderId }} ·
              {{ t('knStance_' + span.stance) }} · {{ span.partIndex }}:{{ span.start }}–{{
                span.end
              }}
            </li>
          </ul>
        </details>
      </article>
      <details v-if="result.supportClosure.records.length" open>
        <summary>{{ t('knInheritedRecords') }}</summary>
        <p class="text-sm text-muted">{{ t('knInheritedHelp') }}</p>
        <article
          v-for="item in result.supportClosure.records"
          :key="item.ref.id"
          class="my-2 rounded border border-default p-3"
        >
          <p class="break-all">
            {{ item.ref.id }} / v{{ item.ref.version }} / {{ item.ref.deletionEpoch }}
          </p>
          <p v-if="item.record.internalUse">{{ t('knInternalCredential') }}</p>
          <p v-else class="whitespace-pre-wrap">{{ item.record.content }}</p>
          <p>
            {{ t('knBacking_' + item.record.status) }} ·
            {{ item.record.epistemicKind || t('knUnknown') }} · {{ item.record.kind }}
          </p>
          <p>
            {{ t('knValidity') }}: {{ date(item.record.validFrom) }} →
            {{ date(item.record.validUntil) }}
          </p>
          <p>
            {{ t('knOccurrence') }}: {{ date(item.record.occurredAt) }} · {{ t('knLearned') }}:
            {{ date(item.record.learnedAt) }}
          </p>
          <ul>
            <li v-for="source in item.record.sources" :key="source.messageId" class="break-all">
              {{ source.sceneId }} / {{ source.messageId }} / {{ source.senderId }}
            </li>
          </ul>
        </article>
      </details>
      <details v-if="result.paths.length">
        <summary>{{ t('knPaths') }}</summary>
        <p class="text-sm">{{ t('knPathsHelp') }}</p>
        <ol>
          <li v-for="(path, index) in result.paths" :key="index" class="break-all">
            {{ path.join(' → ') }}
          </li>
        </ol>
      </details>
    </template>
  </section>
</template>
