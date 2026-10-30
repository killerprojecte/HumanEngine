<script setup lang="ts">
import type { components } from '~/types/api'
type Scene = components['schemas']['Scene']
type Person = { personId: string; displayName?: string; recordCount: number }
type PersonRecord = {
  id: string
  content: string
  kind: string
  learnedAt: string
  observedAt?: string
  subjectEvidence?: {
    coverage: 'complete' | 'limited'
    witnesses: Array<{
      kind: string
      personId?: string
      subjectDisplayName?: string
      sources: Array<{
        sceneId: string
        messageId: string
        senderId: string
        senderDisplayName?: string
      }>
      observations: Array<{
        sceneId: string
        messageId: string
        senderId: string
        sentAt?: string
        receivedAt?: string
      }>
    }>
  }
  occurredAt?: string
  validFrom?: string
  epistemicKind?: string
  validUntil?: string
  status: string
  origin: string
  lineageStatus?: string
  sources: Array<{ sceneId: string; messageId: string; senderId: string }>
}
type Profile = {
  subjectEvidenceCoverage: 'complete' | 'limited'
  sceneId: string
  personId: string
  displayName?: string
  records: PersonRecord[]
}
const props = defineProps<{ scenes: Scene[] }>()
const { t, locale } = useI18n()
const { authenticatedFetch, authenticated } = useApi()
const sceneId = ref(''),
  selectedId = ref('')
const people = ref<Person[]>([]),
  profile = ref<Profile>()
const listBusy = ref(false),
  detailBusy = ref(false),
  listError = ref(false),
  detailError = ref(false)
const realScenes = computed(() =>
  props.scenes.filter((scene) => ['group', 'private'].includes(scene.kind)),
)
let listController: AbortController | undefined, detailController: AbortController | undefined
let listEpoch = 0,
  detailEpoch = 0,
  disposed = false
const personName = (person: Person, index: number) =>
  person.displayName || t('mpUnnamed', { number: index + 1 })
const selectedName = computed(() => {
  const index = people.value.findIndex((person) => person.personId === selectedId.value)
  return index < 0 ? t('mpUnknownPerson') : personName(people.value[index]!, index)
})
const date = (value?: string) => {
  if (!value || !Number.isFinite(Date.parse(value))) return t('mpUnknownTime')
  return new Date(value).toLocaleString(locale.value)
}
function clearDetail() {
  detailEpoch++
  detailController?.abort()
  detailBusy.value = false
  detailError.value = false
  profile.value = undefined
}
async function loadPeople() {
  const epoch = ++listEpoch
  listController?.abort()
  clearDetail()
  people.value = []
  selectedId.value = ''
  listError.value = false
  listBusy.value = false
  if (disposed || !authenticated.value || !sceneId.value) return
  const currentScene = sceneId.value
  listController = new AbortController()
  listBusy.value = true
  try {
    const response = await authenticatedFetch(
      `/api/memory-people?${new URLSearchParams({ sceneId: currentScene })}`,
      { signal: listController.signal },
    )
    if (!response.ok) throw new Error('request_failed')
    const value = (await response.json()) as { sceneId: string; people: Person[] }
    if (disposed || epoch !== listEpoch || !authenticated.value || value.sceneId !== currentScene)
      return
    people.value = value.people
  } catch {
    if (!disposed && epoch === listEpoch && authenticated.value) listError.value = true
  } finally {
    if (epoch === listEpoch) listBusy.value = false
  }
}
async function loadProfile() {
  clearDetail()
  if (disposed || !authenticated.value || !sceneId.value || !selectedId.value) return
  const epoch = detailEpoch
  const currentScene = sceneId.value,
    currentPerson = selectedId.value
  detailController = new AbortController()
  detailBusy.value = true
  try {
    const response = await authenticatedFetch(
      `/api/memory-people/profile?${new URLSearchParams({ sceneId: currentScene, personId: currentPerson })}`,
      { signal: detailController.signal },
    )
    if (!response.ok) throw new Error('request_failed')
    const value = (await response.json()) as Profile
    if (
      disposed ||
      epoch !== detailEpoch ||
      !authenticated.value ||
      value.sceneId !== currentScene ||
      value.personId !== currentPerson
    )
      return
    profile.value = value
  } catch {
    if (!disposed && epoch === detailEpoch && authenticated.value) detailError.value = true
  } finally {
    if (epoch === detailEpoch) detailBusy.value = false
  }
}
watch([sceneId, authenticated], () => void loadPeople())
watch(selectedId, () => void loadProfile())
watch(realScenes, (scenes) => {
  if (sceneId.value && !scenes.some((scene) => scene.id === sceneId.value)) sceneId.value = ''
})
onBeforeUnmount(() => {
  disposed = true
  listEpoch++
  listController?.abort()
  clearDetail()
})
defineExpose({ refresh: loadPeople })
</script>
<template>
  <section class="rounded-lg border border-default p-4 space-y-4" :aria-label="t('mpTitle')">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 class="font-semibold">{{ t('mpTitle') }}</h2>
        <details class="text-xs text-muted">
          <summary class="cursor-pointer w-fit">{{ t('mxAboutView') }}</summary>
          <p class="mt-2 max-w-3xl leading-5">{{ t('mpHelp') }}</p>
        </details>
      </div>
      <UButton
        icon="i-lucide-refresh-cw"
        variant="outline"
        :disabled="!sceneId || !authenticated || listBusy"
        @click="loadPeople"
        >{{ t('refresh') }}</UButton
      >
    </div>
    <label class="block space-y-1">
      <span class="text-sm">{{ t('mpScene') }}</span>
      <select
        v-model="sceneId"
        :disabled="!authenticated"
        class="block w-full rounded-md border border-default bg-default p-2"
      >
        <option value="">{{ t('mpChooseScene') }}</option>
        <option v-for="scene in realScenes" :key="scene.id" :value="scene.id">
          {{ scene.name || t('mpUnnamedScene') }}
        </option>
      </select>
    </label>
    <p v-if="!authenticated" class="text-sm text-muted">{{ t('mpSignIn') }}</p>
    <p v-else-if="listBusy" role="status">{{ t('uxLoading') }}</p>
    <div v-else-if="listError" role="alert" class="text-sm text-error">
      {{ t('networkError') }}
      <UButton variant="link" @click="loadPeople">{{ t('mpRetry') }}</UButton>
    </div>
    <p v-else-if="sceneId && !people.length" class="text-sm text-muted">{{ t('mpNoPeople') }}</p>
    <div v-else-if="people.length" class="grid md:grid-cols-[220px_minmax(0,1fr)] gap-4">
      <nav class="space-y-1" :aria-label="t('mpPersonList')">
        <button
          v-for="(person, index) in people"
          :key="person.personId"
          type="button"
          class="block w-full rounded-md border p-3 text-left"
          :class="
            selectedId === person.personId ? 'border-primary bg-primary/10' : 'border-default'
          "
          :aria-pressed="selectedId === person.personId"
          @click="selectedId = person.personId"
        >
          <span class="block font-medium break-words">{{ personName(person, index) }}</span>
          <span class="text-xs text-muted">{{
            t('mpRecordCount', { count: person.recordCount })
          }}</span>
        </button>
      </nav>
      <div class="min-w-0 space-y-3">
        <p v-if="!selectedId" class="text-sm text-muted">{{ t('mpChoosePerson') }}</p>
        <p v-else-if="detailBusy" role="status">{{ t('uxLoading') }}</p>
        <div v-else-if="detailError" role="alert" class="text-error text-sm">
          {{ t('networkError') }}
          <UButton variant="link" @click="loadProfile">{{ t('mpRetry') }}</UButton>
        </div>
        <template v-else-if="profile">
          <h3 class="font-semibold">{{ selectedName }}</h3>
          <p class="text-xs text-muted">{{ t('mpTimelineHelp') }}</p>
          <p v-if="profile.subjectEvidenceCoverage === 'limited'" class="text-xs text-muted">
            {{ t('mpSubjectBatchLimited') }}
          </p>
          <p v-if="!profile.records.length" class="text-sm text-muted">{{ t('mpNoRecords') }}</p>
          <article
            v-for="record in profile.records"
            :key="record.id"
            class="rounded-md border border-default p-3 space-y-2"
          >
            <div class="flex gap-2 flex-wrap text-xs text-muted">
              <span>{{ t('memoryKind_' + record.kind) }}</span>
              <span>{{
                t(
                  record.epistemicKind
                    ? 'mpEpistemic_' + record.epistemicKind
                    : 'mpEpistemic_unknown',
                )
              }}</span>
              <span>{{ t('mpStatus_' + record.status) }}</span
              ><span>{{ t('mpOrigin_' + record.origin) }}</span>
            </div>
            <p class="text-xs text-muted">{{ t('mpSubjectLabel') }}: {{ selectedName }}</p>
            <p class="whitespace-pre-wrap break-words">{{ record.content }}</p>
            <p class="text-xs text-muted">{{ t('mpQualificationHelp') }}</p>
            <dl class="grid sm:grid-cols-2 gap-2 text-xs">
              <div>
                <dt class="text-muted">{{ t('mpObservedTime') }}</dt>
                <dd>
                  {{ date(record.observedAt)
                  }}<span class="block text-muted">{{ t('mpObservedHelp') }}</span>
                </dd>
              </div>
              <div>
                <dt class="text-muted">{{ t('mpValidFrom') }}</dt>
                <dd>{{ date(record.validFrom) }}</dd>
              </div>
              <div>
                <dt class="text-muted">{{ t('mpLearnedTime') }}</dt>
                <dd>{{ date(record.learnedAt) }}</dd>
              </div>
              <div>
                <dt class="text-muted">{{ t('mpOccurred') }}</dt>
                <dd>{{ date(record.occurredAt) }}</dd>
              </div>
              <div>
                <dt class="text-muted">{{ t('mpValidUntil') }}</dt>
                <dd>{{ date(record.validUntil) }}</dd>
              </div>
            </dl>
            <details class="text-xs text-muted">
              <summary class="cursor-pointer">{{ t('mpSourceDetails') }}</summary>
              <p class="mt-2">{{ t('memorySourceWarning') }}</p>
              <p class="mt-2">{{ t('mpSubjectBindingHelp') }}</p>
              <p v-if="!record.subjectEvidence?.witnesses.length">
                {{
                  t(
                    !record.subjectEvidence && profile.subjectEvidenceCoverage === 'limited'
                      ? 'mpSubjectNotExpanded'
                      : 'mpSubjectEvidenceUnknown',
                  )
                }}
              </p>
              <p v-if="record.subjectEvidence?.coverage === 'limited'">
                {{ t('mpSubjectEvidenceLimited') }}
              </p>
              <div
                v-for="(witness, index) in record.subjectEvidence?.witnesses ?? []"
                :key="index"
                class="mt-2 rounded border border-default p-2 space-y-1"
              >
                <p>{{ t('mpWitness_' + witness.kind) }}</p>
                <p>
                  {{ t('mpSubjectLabel') }}:
                  {{
                    witness.subjectDisplayName ||
                    (witness.personId === profile.personId ? selectedName : t('mpUnknownPerson'))
                  }}
                </p>
                <p v-for="source in witness.sources" :key="source.sceneId + ':' + source.messageId">
                  {{ t('mpSourceSpeaker') }}: {{ source.senderDisplayName || t('mpUnknownPerson') }}
                  <span class="block break-all"
                    >{{ t('mpSourceMessage') }}: {{ source.messageId }}</span
                  >
                </p>
                <dl
                  v-for="source in witness.observations"
                  :key="source.sceneId + ':' + source.messageId"
                  class="space-y-1"
                >
                  <div>
                    <dt>{{ t('mpSourceSentTime') }}</dt>
                    <dd>{{ date(source.sentAt) }}</dd>
                  </div>
                  <div>
                    <dt>{{ t('mpSourceReceivedTime') }}</dt>
                    <dd>{{ date(source.receivedAt) }}</dd>
                  </div>
                </dl>
              </div>
              <p v-if="!record.sources.length">{{ t('mpManualSource') }}</p>
              <ul v-else class="mt-2 space-y-1">
                <li
                  v-for="source in record.sources"
                  :key="source.sceneId + ':' + source.messageId"
                  class="break-all"
                >
                  {{ source.messageId }}
                </li>
              </ul>
              <p class="mt-2 break-all">{{ t('mpIdentity') }}: {{ profile.personId }}</p>
            </details>
            <MemoryRevisionHistory :scene-id="sceneId" :memory-id="record.id" />
          </article>
        </template>
      </div>
    </div>
    <MemoryIdentityLinks :scenes="props.scenes" />
  </section>
</template>
