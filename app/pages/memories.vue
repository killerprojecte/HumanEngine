<script setup lang="ts">
import MemoryRelations from '~/components/memory/MemoryRelations.vue'
import type { components } from '~/types/api'
type Memory = components['schemas']['Memory']
type Scene = components['schemas']['Scene']
const { t, locale } = useI18n()
const { client, authenticatedFetch } = useApi()
const memories = ref<Memory[]>([]),
  scenes = ref<Scene[]>([])
const error = ref(''),
  busy = ref(false),
  loaded = ref(false),
  saved = ref(false)
const sceneFilter = ref('all'),
  personFilter = ref(''),
  query = ref(''),
  kindFilter = ref('all'),
  statusFilter = ref('all'),
  page = ref(1)
const pageSize = 20
const recordsRegion = ref<HTMLElement>()
const section = ref('records'),
  knowledgeTab = ref('knowledge'),
  activityTab = ref('commitments')
const visited = reactive(new Set(['records']))
watch(section, (value) => visited.add(value))
const sections = computed(() => [
  { label: t('mxRecords'), value: 'records', icon: 'i-lucide-search' },
  { label: t('mxPeople'), value: 'people', icon: 'i-lucide-users' },
  { label: t('mxKnowledge'), value: 'knowledge', icon: 'i-lucide-network' },
  { label: t('mxRelations'), value: 'relations', icon: 'i-lucide-waypoints' },
  { label: t('mxActivity'), value: 'activity', icon: 'i-lucide-calendar-check' },
])
const helpOpen = ref(false),
  filtersOpen = ref(false)
const people = ref<{ personId: string; displayName?: string; recordCount: number }[]>([])
const personLabels = reactive<Record<string, Record<string, string>>>({})
const labelRequests = new Set<string>()
const scenePeopleRequests = new Map<
  string,
  Promise<{ sceneId: string; people: typeof people.value }>
>()
let labelQueue: string[] = [],
  labelEpoch = 0,
  activeLabelReads = 0,
  labelsDisposed = false
const peopleBusy = ref(false),
  peopleError = ref(false)
let peopleEpoch = 0
const scopedPeople = computed(() => {
  const ids = new Set(
    memories.value.flatMap((memory) =>
      memory.scope.kind === 'scene' &&
      memory.scope.sceneId === currentSceneId.value &&
      memory.scope.personId
        ? [memory.scope.personId]
        : [],
    ),
  )
  return [...ids].map((personId) => ({
    personId,
    displayName: people.value.find((person) => person.personId === personId)?.displayName,
  }))
})
const personOptions = computed(() => [
  { label: t('mxAllPeople'), value: '' },
  ...scopedPeople.value.map((person, index) => ({
    label: person.displayName
      ? scopedPeople.value.filter((other) => other.displayName === person.displayName).length > 1
        ? `${person.displayName} · ${index + 1}`
        : person.displayName
      : t('mpUnnamed', { number: index + 1 }),
    value: person.personId,
  })),
])
function personName(memory: Memory) {
  if (memory.scope.kind !== 'scene' || !memory.scope.personId) return ''
  const { sceneId, personId } = memory.scope
  return (
    personLabels[sceneId]?.[personId] ||
    (personId.length > 18 ? `${personId.slice(0, 8)}…${personId.slice(-4)}` : personId)
  )
}
function rememberPeople(sceneId: string, rows: typeof people.value) {
  personLabels[sceneId] = Object.fromEntries(
    rows
      .filter((person) => person.displayName)
      .map((person) => [person.personId, person.displayName!]),
  )
}
function readScenePeople(sceneId: string) {
  const existing = scenePeopleRequests.get(sceneId)
  if (existing) return existing
  const request = (async () => {
    const response = await authenticatedFetch(
      `/api/memory-people?${new URLSearchParams({ sceneId })}`,
    )
    if (!response.ok) throw new Error('read')
    const data = (await response.json()) as { sceneId: string; people: typeof people.value }
    if (data.sceneId !== sceneId) throw new Error('scope')
    return data
  })()
  scenePeopleRequests.set(sceneId, request)
  void request
    .finally(() => {
      if (scenePeopleRequests.get(sceneId) === request) scenePeopleRequests.delete(sceneId)
    })
    .catch(() => {})
  return request
}
function loadVisiblePersonNames() {
  labelQueue = [
    ...new Set(
      pageItems.value.flatMap((memory) =>
        memory.scope.kind === 'scene' && memory.scope.personId ? [memory.scope.sceneId] : [],
      ),
    ),
  ].filter((id) => !personLabels[id] && !labelRequests.has(id))
  pumpPersonLabels()
}
function pumpPersonLabels() {
  if (labelsDisposed) return
  // One shared queue bounds overlapping page/filter changes, not just one request batch.
  while (activeLabelReads < 4 && labelQueue.length) {
    const sceneId = labelQueue.shift()!
    if (personLabels[sceneId] || labelRequests.has(sceneId)) continue
    labelRequests.add(sceneId)
    const epoch = labelEpoch
    activeLabelReads++
    void (async () => {
      try {
        const data = await readScenePeople(sceneId)
        if (!labelsDisposed && epoch === labelEpoch && data.sceneId === sceneId)
          rememberPeople(sceneId, data.people)
      } catch {
        /* A missing label never hides an administration record or changes its scope. */
      } finally {
        activeLabelReads--
        pumpPersonLabels()
      }
    })()
  }
}
async function loadPeople() {
  const epoch = ++peopleEpoch
  people.value = []
  peopleError.value = false
  peopleBusy.value = false
  if (!currentSceneId.value) return
  peopleBusy.value = true
  const requestedScene = currentSceneId.value
  try {
    const data = await readScenePeople(requestedScene)
    if (epoch === peopleEpoch) {
      people.value = data.people
      rememberPeople(requestedScene, data.people)
    }
  } catch {
    if (epoch === peopleEpoch) peopleError.value = true
  } finally {
    if (epoch === peopleEpoch) peopleBusy.value = false
  }
}
watch([sceneFilter, kindFilter, statusFilter, personFilter, query], () => {
  page.value = 1
})
watch(sceneFilter, loadPeople)
const modalOpen = ref(false),
  mode = ref<'view' | 'edit' | 'new'>('view'),
  selectedId = ref('')
const selected = computed(() => memories.value.find((memory) => memory.id === selectedId.value))
const currentSceneId = computed(() =>
  sceneFilter.value.startsWith('scene:') ? sceneFilter.value.slice(6) : '',
)
const currentScene = computed(() => scenes.value.find((scene) => scene.id === currentSceneId.value))
const sceneName = (id: string) => scenes.value.find((scene) => scene.id === id)?.name ?? id
const sceneLabel = (scene: Scene) =>
  `${t(scene.kind === 'group' ? 'scGroup' : 'scPrivate')} · ${scene.name}`
const sceneOptions = computed(() =>
  scenes.value.map((scene) => ({ label: sceneLabel(scene), value: scene.id })),
)
const activeScopeLabel = computed(() =>
  sceneFilter.value === 'all'
    ? t('mmAllScopes')
    : sceneFilter.value === 'public'
      ? t('memoryPublic')
      : currentScene.value
        ? sceneLabel(currentScene.value)
        : currentSceneId.value,
)
const fresh = () => ({
  content: '',
  kind: 'fact' as Memory['kind'],
  importance: 'medium' as Memory['importance'],
  confidence: 1,
  locked: false,
  scope: 'scene',
  sceneId: '',
  personId: '',
  status: 'active' as Memory['status'],
})
const draft = ref(fresh()),
  publicConfirmed = ref(false)
let openingDraft = '',
  original: Memory | undefined
const dirty = computed(
  () => modalOpen.value && mode.value !== 'view' && JSON.stringify(draft.value) !== openingDraft,
)
const confirmation = ref<'discard' | 'delete' | 'lock' | 'revalidate'>()
const confirmationKind = ref<'discard' | 'delete' | 'lock' | 'revalidate'>('discard')
const confirmationPreview = ref('')
const confirmationCopy = ref({ title: '', description: '', action: '' })
const confirmationError = ref('')
const recoveryRequired = ref(false),
  editorUncertain = ref(false)
class MutationRejected extends Error {}
function mutationFailure(response: Response, message: string): never {
  if (response.status >= 400 && response.status < 500) throw new MutationRejected(message)
  throw new Error(message)
}
function trackUncertain(cause: unknown) {
  if (!(cause instanceof MutationRejected)) {
    recoveryRequired.value = true
    editorUncertain.value = true
  }
}
let pendingNavigation: ((allow: boolean) => void) | undefined
watch(sceneFilter, () => {
  personFilter.value = ''
})
watch(
  () => draft.value.sceneId,
  (next, previous) => {
    if (modalOpen.value && mode.value === 'new' && next !== previous) draft.value.personId = ''
  },
)
watch(
  () => draft.value.scope,
  () => {
    publicConfirmed.value = false
    if (mode.value === 'new') draft.value.personId = ''
  },
)
const visible = computed(() =>
  memories.value.filter((memory) => {
    const scope = memory.scope
    return (
      (sceneFilter.value === 'all' ||
        (sceneFilter.value === 'public' && scope.kind === 'sharedPublicKnowledge') ||
        (scope.kind === 'scene' && scope.sceneId === currentSceneId.value)) &&
      (!personFilter.value.trim() ||
        (!!currentSceneId.value &&
          scope.kind === 'scene' &&
          scope.sceneId === currentSceneId.value &&
          scope.personId === personFilter.value.trim())) &&
      (kindFilter.value === 'all' || memory.kind === kindFilter.value) &&
      (statusFilter.value === 'all' || memory.status === statusFilter.value) &&
      (!query.value.trim() ||
        memory.content.toLocaleLowerCase().includes(query.value.trim().toLocaleLowerCase()))
    )
  }),
)
const ordered = computed(() =>
  [...visible.value].sort((a, b) => b.learnedAt.localeCompare(a.learnedAt)),
)
const pageCount = computed(() => Math.max(1, Math.ceil(ordered.value.length / pageSize)))
const pageItems = computed(() =>
  ordered.value.slice((page.value - 1) * pageSize, page.value * pageSize),
)
watch(pageItems, () => {
  void loadVisiblePersonNames()
})
watch(pageCount, (count) => {
  if (page.value > count) page.value = count
})
watch(page, async () => {
  if (section.value !== 'records' || modalOpen.value || filtersOpen.value) return
  const fromPagination = !!document.activeElement?.closest('[data-memory-pagination]')
  await nextTick()
  recordsRegion.value?.scrollIntoView({ block: 'start', behavior: 'auto' })
  if (fromPagination) recordsRegion.value?.focus({ preventScroll: true })
})
const filtersActive = computed(
  () =>
    !!query.value ||
    sceneFilter.value !== 'all' ||
    kindFilter.value !== 'all' ||
    statusFilter.value !== 'all' ||
    !!personFilter.value,
)
const scopeFilterOptions = computed(() => [
  { label: t('mmAllScopes'), value: 'all' },
  { label: t('memoryPublic'), value: 'public' },
  ...sceneOptions.value.map((scene) => ({ label: scene.label, value: `scene:${scene.value}` })),
])
const kindFilterOptions = computed(() => [
  { label: t('mxAllKinds'), value: 'all' },
  ...['fact', 'claim', 'term', 'experience', 'summary'].map((value) => ({
    label: t(`memoryKind_${value}`),
    value,
  })),
])
const statusFilterOptions = computed(() => [
  { label: t('mxAllStatuses'), value: 'all' },
  ...['active', 'disputed', 'superseded', 'invalidated'].map((value) => ({
    label: t(`memoryStatus_${value}`),
    value,
  })),
])
const filterChips = computed(() => [
  ...(sceneFilter.value !== 'all' ? [activeScopeLabel.value] : []),
  ...(kindFilter.value !== 'all' ? [t(`memoryKind_${kindFilter.value}`)] : []),
  ...(statusFilter.value !== 'all' ? [t(`memoryStatus_${statusFilter.value}`)] : []),
  ...(personFilter.value
    ? [
        personOptions.value.find((person) => person.value === personFilter.value)?.label ??
          personFilter.value,
      ]
    : []),
])
function focusAfterFilters(event: Event) {
  const trigger = recordsRegion.value?.querySelector<HTMLElement>('[data-memory-filter-trigger]')
  if (!trigger?.getClientRects().length) {
    // Rotation can hide the mobile trigger while its sheet is still open.
    event.preventDefault()
    recordsRegion.value?.focus({ preventScroll: true })
  }
}
function resetFilters() {
  query.value = ''
  sceneFilter.value = 'all'
  kindFilter.value = 'all'
  statusFilter.value = 'all'
  personFilter.value = ''
}
const canSave = computed(
  () =>
    !!draft.value.content.trim() &&
    (mode.value !== 'new' ||
      (Number.isFinite(Number(draft.value.confidence)) &&
        Number(draft.value.confidence) >= 0 &&
        Number(draft.value.confidence) <= 1 &&
        (draft.value.scope === 'public' ? publicConfirmed.value : !!draft.value.sceneId))),
)
function scopeLabel(memory: Memory) {
  const scope = memory.scope
  if (scope.kind === 'sharedPublicKnowledge') return t('memoryPublic')
  const scene = scenes.value.find((scene) => scene.id === scope.sceneId)
  return scene ? sceneLabel(scene) : scope.sceneId
}
const modalTitle = computed(() =>
  t(mode.value === 'view' ? 'mmDetails' : mode.value === 'new' ? 'memoryCreate' : 'memoryEdit'),
)
const modalDescription = computed(() =>
  mode.value === 'new' ? t('mmCreateHelp') : selected.value ? scopeLabel(selected.value) : '',
)
async function load(reconcile = false) {
  if (busy.value || (modalOpen.value && !reconcile)) return
  busy.value = true
  error.value = ''
  confirmationError.value = ''
  saved.value = false
  try {
    const [records, places] = await Promise.all([
      client.GET('/api/memories'),
      client.GET('/api/scenes'),
    ])
    if (!records.data || !places.data)
      throw new Error(records.error?.error ?? places.error?.error ?? t('networkError'))
    labelEpoch++
    labelQueue = []
    labelRequests.clear()
    scenePeopleRequests.clear()
    peopleEpoch++
    people.value = []
    peopleBusy.value = false
    if (currentSceneId.value) void loadPeople()
    for (const key of Object.keys(personLabels)) delete personLabels[key]
    memories.value = records.data
    scenes.value = places.data
    loaded.value = true
    recoveryRequired.value = false
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : t('networkError')
    if (confirmation.value) confirmationError.value = error.value
  } finally {
    busy.value = false
  }
}
function openDetails(memory: Memory) {
  if (busy.value || recoveryRequired.value) return
  editorUncertain.value = false
  selectedId.value = memory.id
  mode.value = 'view'
  error.value = ''
  saved.value = false
  confirmation.value = undefined
  modalOpen.value = true
}
function begin(memory?: Memory) {
  if (busy.value || recoveryRequired.value || (modalOpen.value && editorUncertain.value)) return
  editorUncertain.value = false
  original = memory ? JSON.parse(JSON.stringify(memory)) : undefined
  draft.value = memory
    ? {
        ...fresh(),
        content: memory.content,
        kind: memory.kind,
        importance: memory.importance,
        confidence: memory.confidence,
        locked: memory.locked,
        status: memory.status,
        scope: memory.scope.kind === 'scene' ? 'scene' : 'public',
        sceneId: memory.scope.kind === 'scene' ? memory.scope.sceneId : '',
        personId: memory.scope.kind === 'scene' ? (memory.scope.personId ?? '') : '',
      }
    : {
        ...fresh(),
        scope: sceneFilter.value === 'public' ? 'public' : 'scene',
        sceneId: currentSceneId.value || sceneOptions.value[0]?.value || '',
      }
  openingDraft = JSON.stringify(draft.value)
  selectedId.value = memory?.id ?? ''
  mode.value = memory ? 'edit' : 'new'
  publicConfirmed.value = false
  error.value = ''
  saved.value = false
  confirmation.value = undefined
  modalOpen.value = true
}
function ask(kind: 'discard' | 'delete' | 'lock' | 'revalidate') {
  if (busy.value || (kind !== 'discard' && (recoveryRequired.value || editorUncertain.value)))
    return
  confirmationError.value = ''
  confirmationCopy.value =
    kind === 'revalidate'
      ? {
          title: t('memoryRevalidate'),
          description: t('memoryRevalidateConfirm'),
          action: t('memoryRevalidate'),
        }
      : kind === 'discard'
        ? { title: t('mcDiscardTitle'), description: t('mmDiscardHelp'), action: t('mcDiscard') }
        : kind === 'delete'
          ? {
              title: t('mmDeleteTitle'),
              description: t('memoryDeleteConfirm'),
              action: t('confirmDelete'),
            }
          : {
              title: t(selected.value?.locked ? 'mmUnlockTitle' : 'mmLockTitle'),
              description: t('mmLockHelp'),
              action: t(selected.value?.locked ? 'memoryUnlock' : 'memoryLock'),
            }
  confirmationKind.value = kind
  confirmationPreview.value = selected.value?.content ?? ''
  confirmation.value = kind
}
function closeModal() {
  modalOpen.value = false
  confirmation.value = undefined
  error.value = ''
}
function requestClose(open = false) {
  if (open || busy.value) return
  if (dirty.value) ask('discard')
  else closeModal()
}
function cancelConfirmation() {
  if (busy.value) return
  confirmation.value = undefined
  pendingNavigation?.(false)
  pendingNavigation = undefined
}
function discard() {
  closeModal()
  pendingNavigation?.(true)
  pendingNavigation = undefined
}
onBeforeRouteLeave(() => {
  if (busy.value) return false
  if (!dirty.value) return true
  ask('discard')
  return new Promise<boolean>((resolve) => {
    pendingNavigation = resolve
  })
})
function warnBeforeUnload(event: BeforeUnloadEvent) {
  if (!dirty.value) return
  event.preventDefault()
  event.returnValue = ''
}
onMounted(() => {
  void load()
  window.addEventListener('beforeunload', warnBeforeUnload)
})
onBeforeUnmount(() => {
  labelsDisposed = true
  labelEpoch++
  peopleEpoch++
  labelQueue = []
  pendingNavigation?.(false)
  window.removeEventListener('beforeunload', warnBeforeUnload)
})
function replace(memory: Memory) {
  const index = memories.value.findIndex((item) => item.id === memory.id)
  if (index < 0) memories.value.unshift(memory)
  else memories.value[index] = memory
}
async function save() {
  if (
    busy.value ||
    recoveryRequired.value ||
    editorUncertain.value ||
    !canSave.value ||
    mode.value === 'view'
  )
    return
  busy.value = true
  error.value = ''
  try {
    let record: Memory | undefined
    if (mode.value === 'new') {
      const response = await client.POST('/api/memories', {
        body: {
          scope:
            draft.value.scope === 'public'
              ? { kind: 'sharedPublicKnowledge' }
              : {
                  kind: 'scene',
                  sceneId: draft.value.sceneId,
                  ...(draft.value.personId.trim() ? { personId: draft.value.personId.trim() } : {}),
                },
          content: draft.value.content.trim(),
          kind: draft.value.kind,
          importance: draft.value.importance,
          confidence: Number(draft.value.confidence),
          locked: draft.value.locked,
        },
      })
      if (!response.data)
        mutationFailure(response.response, response.error?.error ?? t('networkError'))
      record = response.data
      sceneFilter.value =
        record.scope.kind === 'sharedPublicKnowledge' ? 'public' : `scene:${record.scope.sceneId}`
      personFilter.value = ''
      query.value = ''
      kindFilter.value = 'all'
      statusFilter.value = 'all'
      section.value = 'records'
    } else if (original) {
      const patch: { content?: string; status?: Memory['status']; locked?: boolean } = {}
      if (draft.value.content.trim() !== original.content)
        patch.content = draft.value.content.trim()
      if (draft.value.status !== original.status) patch.status = draft.value.status
      if (draft.value.locked !== original.locked) patch.locked = draft.value.locked
      if (!Object.keys(patch).length) {
        closeModal()
        return
      }
      const response = await client.PATCH('/api/memories/{id}', {
        params: { path: { id: original.id } },
        body: {
          ...patch,
          ...(original.revision
            ? {
                guard: {
                  expectedVersion: original.revision.version,
                  expectedDeletionEpoch: original.revision.deletionEpoch,
                  idempotencyKey: crypto.randomUUID(),
                },
              }
            : {}),
        },
      })
      if (!response.data)
        mutationFailure(response.response, response.error?.error ?? t('networkError'))
      record = response.data
    }
    if (record) {
      replace(record)
      selectedId.value = record.id
      saved.value = true
      closeModal()
    }
  } catch (cause) {
    trackUncertain(cause)
    error.value = cause instanceof Error ? cause.message : t('networkError')
  } finally {
    busy.value = false
  }
}
async function confirmAction() {
  if (busy.value || !confirmation.value) return
  if (confirmation.value === 'discard') {
    discard()
    return
  }
  if (recoveryRequired.value || editorUncertain.value) return
  const record = selected.value
  if (!record) return
  const action = confirmation.value
  busy.value = true
  confirmationError.value = ''
  try {
    if (action === 'delete') {
      const response = await client.DELETE('/api/memories/{id}', {
        params: { path: { id: record.id } },
      })
      if (!response.data?.ok)
        mutationFailure(response.response, response.error?.error ?? t('networkError'))
      memories.value = memories.value.filter((memory) => memory.id !== record.id)
      closeModal()
    } else {
      const response = await client.PATCH('/api/memories/{id}', {
        params: { path: { id: record.id } },
        body: {
          ...(action === 'revalidate'
            ? { content: record.content, status: 'active' as const }
            : { locked: !record.locked }),
          ...(record.revision
            ? {
                guard: {
                  expectedVersion: record.revision.version,
                  expectedDeletionEpoch: record.revision.deletionEpoch,
                  idempotencyKey: crypto.randomUUID(),
                },
              }
            : {}),
        },
      })
      if (!response.data)
        mutationFailure(response.response, response.error?.error ?? t('networkError'))
      replace(response.data)
      confirmation.value = undefined
    }
  } catch (cause) {
    trackUncertain(cause)
    confirmationError.value = cause instanceof Error ? cause.message : t('networkError')
  } finally {
    busy.value = false
  }
}
function date(value: string) {
  return new Date(value).toLocaleString(locale.value)
}
</script>
<template>
  <div class="memory-page space-y-2 sm:space-y-4 min-w-0">
    <header class="flex flex-wrap sm:flex-nowrap items-center justify-between gap-2 sm:gap-3">
      <div class="flex items-center gap-1.5 sm:gap-3 min-w-0">
        <h1 class="page-heading">{{ t('memoryTitle') }}</h1>
        <UBadge
          v-if="loaded"
          color="neutral"
          variant="subtle"
          class="tabular"
          :aria-label="t('mxTotal', { count: memories.length })"
          ><span class="sm:hidden">{{ memories.length }}</span
          ><span class="hidden sm:inline">{{
            t('mxTotal', { count: memories.length })
          }}</span></UBadge
        >
      </div>
      <div class="flex shrink-0 items-center gap-1 sm:gap-2">
        <UButton
          v-if="section === 'records'"
          class="hidden sm:inline-flex"
          variant="ghost"
          color="neutral"
          icon="i-lucide-refresh-cw"
          :aria-label="t('refresh')"
          :disabled="busy || modalOpen"
          :loading="busy"
          @click="load()"
        />
        <EmbeddingIndexPanel :config-busy="busy" compact />
        <UDropdownMenu
          :items="[
            {
              label: t('refresh'),
              icon: 'i-lucide-refresh-cw',
              class: 'sm:hidden',
              disabled: busy || modalOpen || section !== 'records',
              onSelect: () => load(),
            },
            {
              label: t('mxGuide'),
              icon: 'i-lucide-circle-help',
              onSelect: () => (helpOpen = true),
            },
          ]"
        >
          <UButton
            variant="outline"
            color="neutral"
            icon="i-lucide-ellipsis"
            class="min-w-11 sm:min-w-0 justify-center"
            :aria-label="t('mcActions')"
          />
        </UDropdownMenu>
        <UButton
          icon="i-lucide-plus"
          class="min-w-11 sm:min-w-0 justify-center"
          :aria-label="t('mxCreate')"
          :disabled="busy || !loaded || recoveryRequired"
          @click="begin()"
          ><span class="hidden sm:inline">{{ t('mxCreate') }}</span></UButton
        >
      </div>
    </header>
    <UAlert v-if="error && !modalOpen" color="error" :title="error" /><UAlert
      v-if="saved"
      color="success"
      variant="subtle"
      :title="t('mmSaved')"
    />
    <div v-if="recoveryRequired && !modalOpen" class="space-y-3 mb-4">
      <UAlert
        color="warning"
        :title="t('mrUnknownTitle')"
        :description="t(recoveryRequired ? 'mrUnknownHelp' : 'mrReloadedHelp')"
      /><UButton
        v-if="recoveryRequired"
        variant="outline"
        color="neutral"
        :loading="busy"
        :disabled="busy"
        @click="load(true)"
        >{{ t('mrReload') }}</UButton
      >
    </div>
    <UTabs
      v-model="section"
      :items="sections.map((item) => ({ ...item, slot: item.value }))"
      :unmount-on-hide="false"
      variant="link"
      :ui="{
        list: 'memory-tabs-list overflow-x-auto overflow-y-hidden flex-nowrap w-full',
        trigger: 'shrink-0',
        content: 'min-w-0 pt-1 sm:pt-2',
      }"
      :aria-label="t('mxNavigation')"
    >
      <template #records>
        <section
          ref="recordsRegion"
          tabindex="-1"
          class="surface overflow-hidden focus:outline-none"
          :aria-label="t('mxRecords')"
        >
          <div class="p-3 border-b border-default space-y-3">
            <div class="flex flex-wrap items-center gap-2">
              <UInput
                v-model="query"
                icon="i-lucide-search"
                :placeholder="t('memorySearch')"
                :aria-label="t('mmSearch')"
                class="min-w-0 sm:min-w-52 flex-1"
              />
              <USlideover
                v-model:open="filtersOpen"
                :content="{ onCloseAutoFocus: focusAfterFilters }"
                side="bottom"
                :title="t('mxFilters')"
                :ui="{
                  content: 'max-h-[85dvh] rounded-t-xl',
                  header: 'p-4 shrink-0',
                  body: 'p-4 min-h-0 overflow-y-auto space-y-4',
                  footer: 'p-4 shrink-0 justify-between',
                }"
              >
                <UButton
                  class="sm:hidden shrink-0"
                  data-memory-filter-trigger
                  icon="i-lucide-sliders-horizontal"
                  color="neutral"
                  variant="outline"
                  :aria-label="t('mxFilters')"
                  >{{ t('mxFilters')
                  }}<span v-if="filterChips.length" class="tabular">{{
                    filterChips.length
                  }}</span></UButton
                >
                <template #body>
                  <UFormField :label="t('memoryScope')"
                    ><USelect
                      v-model="sceneFilter"
                      :items="scopeFilterOptions"
                      :aria-label="t('memoryScope')"
                      class="w-full"
                  /></UFormField>
                  <UFormField :label="t('memoryKind')"
                    ><USelect
                      v-model="kindFilter"
                      :items="kindFilterOptions"
                      :aria-label="t('memoryKind')"
                      class="w-full"
                  /></UFormField>
                  <UFormField :label="t('memoryStatus')"
                    ><USelect
                      v-model="statusFilter"
                      :items="statusFilterOptions"
                      :aria-label="t('memoryStatus')"
                      class="w-full"
                  /></UFormField>
                  <UFormField v-if="currentSceneId" :label="t('mxPersonFilter')">
                    <select
                      v-model="personFilter"
                      :disabled="peopleBusy"
                      :aria-label="t('mxPersonFilter')"
                      class="rounded-md border border-default bg-default px-3 py-2 w-full text-sm"
                    >
                      <option
                        v-for="person in personOptions"
                        :key="person.value"
                        :value="person.value"
                      >
                        {{ person.label }}
                      </option>
                    </select>
                    <UButton
                      v-if="peopleError"
                      variant="link"
                      color="warning"
                      @click="loadPeople"
                      >{{ t('mpRetry') }}</UButton
                    >
                  </UFormField>
                </template>
                <template #footer>
                  <UButton
                    color="neutral"
                    variant="ghost"
                    :disabled="!filtersActive"
                    @click="resetFilters"
                    >{{ t('mxClearFilters') }}</UButton
                  >
                  <UButton @click="filtersOpen = false">{{
                    t('mxShowResults', { count: visible.length })
                  }}</UButton>
                </template>
              </USlideover>
              <USelect
                v-model="sceneFilter"
                :items="scopeFilterOptions"
                :aria-label="t('memoryScope')"
                class="hidden sm:flex w-52"
              />
              <USelect
                v-model="kindFilter"
                :items="kindFilterOptions"
                :aria-label="t('memoryKind')"
                class="hidden sm:flex w-32"
              />
              <USelect
                v-model="statusFilter"
                :items="statusFilterOptions"
                :aria-label="t('memoryStatus')"
                class="hidden sm:flex w-32"
              />
              <UButton
                v-if="filtersActive"
                class="hidden sm:inline-flex"
                variant="ghost"
                color="neutral"
                icon="i-lucide-filter-x"
                :aria-label="t('mxClearFilters')"
                @click="resetFilters"
              />
            </div>
            <div
              v-if="filtersActive"
              class="flex flex-wrap items-center justify-between gap-2 text-xs text-muted"
            >
              <div class="hidden sm:flex flex-wrap items-center gap-2 min-w-0">
                <select
                  v-if="currentSceneId"
                  v-model="personFilter"
                  :disabled="peopleBusy"
                  :aria-label="t('mxPersonFilter')"
                  class="rounded-md border border-default bg-default px-2 py-1.5 w-40 text-sm"
                >
                  <option v-for="person in personOptions" :key="person.value" :value="person.value">
                    {{ person.label }}
                  </option>
                </select>
                <UButton
                  v-if="peopleError"
                  variant="link"
                  color="warning"
                  size="xs"
                  @click="loadPeople"
                  >{{ t('mpRetry') }}</UButton
                >
              </div>
              <div v-if="filterChips.length" class="flex sm:hidden flex-wrap gap-1 min-w-0">
                <UBadge
                  v-for="(label, index) in filterChips"
                  :key="index"
                  color="neutral"
                  variant="subtle"
                  :title="label"
                  ><span class="truncate max-w-44">{{ label }}</span></UBadge
                >
              </div>
              <span class="tabular"
                >{{ t('mxMatchCount', { count: visible.length, total: memories.length }) }}
                <span class="hidden sm:inline"> · {{ t('mxNewestFirst') }}</span></span
              >
            </div>
          </div>
          <div v-if="!loaded && busy" class="p-12 text-center text-muted text-sm" role="status">
            {{ t('mmLoading') }}
          </div>
          <div v-else-if="!loaded && error" class="py-12 px-6 text-center space-y-3">
            <UIcon name="i-lucide-wifi-off" class="size-7 text-muted" />
            <p class="font-medium">{{ t('mxLoadFailed') }}</p>
            <UButton variant="outline" color="neutral" @click="load()">{{ t('mpRetry') }}</UButton>
          </div>
          <div v-else-if="!visible.length" class="py-14 px-6 text-center space-y-3">
            <UIcon name="i-lucide-search" class="size-8 text-dimmed" />
            <p class="font-medium">{{ t(filtersActive ? 'mxNoMatches' : 'mxNoRecords') }}</p>
            <p class="text-sm text-muted">
              {{ t(filtersActive ? 'mxNoMatchesHelp' : 'mmEmptyHelp') }}
            </p>
            <UButton v-if="filtersActive" variant="outline" color="neutral" @click="resetFilters">{{
              t('mxClearFilters')
            }}</UButton>
          </div>
          <template v-else>
            <div
              class="memory-grid hidden md:grid px-4 py-2 bg-elevated/50 text-xs text-muted border-b border-default"
            >
              <span>{{ t('memoryContent') }}</span
              ><span>{{ t('memoryScope') }}</span
              ><span>{{ t('memoryKind') }}</span
              ><span>{{ t('mmLearnedAt') }}</span
              ><span>{{ t('memoryStatus') }}</span
              ><span />
            </div>
            <div class="divide-y divide-default">
              <article
                v-for="memory in pageItems"
                :key="memory.id"
                class="memory-grid group grid px-4 py-3 hover:bg-elevated/35 items-center gap-y-2"
                data-testid="memory-row"
              >
                <div class="min-w-0">
                  <button
                    type="button"
                    class="memory-content-button text-left text-sm font-medium text-highlighted leading-6 line-clamp-2 break-words w-full rounded focus-visible:outline-2 focus-visible:outline-primary focus-visible:outline-offset-2"
                    :disabled="busy || recoveryRequired"
                    @click="openDetails(memory)"
                  >
                    {{ memory.content }}
                  </button>
                  <div class="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted mt-1">
                    <span>{{ t(`memoryOrigin_${memory.origin}`) }}</span>
                    <span
                      >{{ t('memoryConfidence') }} {{ Math.round(memory.confidence * 100) }}%</span
                    >
                    <span v-if="memory.sources.length" class="inline-flex items-center gap-1"
                      ><UIcon name="i-lucide-link" class="size-3" />{{
                        t('mxSources', { count: memory.sources.length })
                      }}</span
                    >
                    <span v-if="memory.locked" class="inline-flex items-center gap-1"
                      ><UIcon name="i-lucide-lock-keyhole" class="size-3" />{{
                        t('mmLocked')
                      }}</span
                    >
                    <span
                      v-if="memory.lineageStatus && memory.lineageStatus !== 'verified'"
                      class="text-warning"
                      >{{ t(`memoryLineage_${memory.lineageStatus}`) }}</span
                    >
                  </div>
                </div>
                <div class="min-w-0 text-xs text-muted">
                  <p class="truncate" :title="scopeLabel(memory)">{{ scopeLabel(memory) }}</p>
                  <p v-if="personName(memory)" class="truncate mt-1">{{ personName(memory) }}</p>
                </div>
                <div class="text-xs text-muted">{{ t(`memoryKind_${memory.kind}`) }}</div>
                <time
                  class="text-xs text-muted tabular"
                  :datetime="memory.learnedAt"
                  :title="date(memory.learnedAt)"
                  >{{
                    new Date(memory.learnedAt).toLocaleString(locale, {
                      month: '2-digit',
                      day: '2-digit',
                      hour: '2-digit',
                      minute: '2-digit',
                      hour12: false,
                    })
                  }}</time
                >
                <div>
                  <UBadge
                    size="sm"
                    :color="memory.status === 'active' ? 'neutral' : 'warning'"
                    variant="subtle"
                    >{{ t(`memoryStatus_${memory.status}`) }}</UBadge
                  >
                </div>
                <UButton
                  variant="ghost"
                  color="neutral"
                  icon="i-lucide-chevron-right"
                  :aria-label="t('mmViewMemory')"
                  class="memory-view-button"
                  :disabled="busy || recoveryRequired"
                  @click="openDetails(memory)"
                />
              </article>
            </div>
            <footer
              data-memory-pagination
              class="flex flex-wrap items-center justify-between gap-2 px-4 py-3 border-t border-default"
            >
              <span class="text-xs text-muted tabular">{{
                t('mxPageRange', {
                  from: (page - 1) * pageSize + 1,
                  to: Math.min(page * pageSize, visible.length),
                  total: visible.length,
                })
              }}</span>
              <UPagination
                v-model:page="page"
                :items-per-page="pageSize"
                :total="visible.length"
                :sibling-count="1"
                size="sm"
                :show-edges="false"
              />
            </footer>
          </template>
        </section>
      </template>
      <template #people
        ><div v-if="visited.has('people')" class="memory-tool-panel">
          <MemoryPeople :scenes="scenes" /></div
      ></template>
      <template #knowledge>
        <div v-if="visited.has('knowledge')" class="memory-tool-panel">
          <UTabs
            v-model="knowledgeTab"
            :unmount-on-hide="false"
            :items="[
              { label: t('knTitle'), value: 'knowledge', slot: 'knowledge' },
              { label: t('smTitle'), value: 'semantics', slot: 'semantics' },
            ]"
            :aria-label="t('mxKnowledge')"
          >
            <template #knowledge><KnowledgePanel :scenes="scenes" /></template>
            <template #semantics><SemanticPanel :scenes="scenes" /></template>
          </UTabs>
        </div>
      </template>
      <template #relations
        ><div v-if="visited.has('relations')" class="memory-tool-panel">
          <MemoryRelations :scenes="scenes" /></div
      ></template>
      <template #activity>
        <div v-if="visited.has('activity')" class="memory-tool-panel">
          <UTabs
            v-model="activityTab"
            :unmount-on-hide="false"
            :items="[
              { label: t('cmTitle'), value: 'commitments', slot: 'commitments' },
              { label: t('exTitle'), value: 'experience', slot: 'experience' },
              { label: t('wwTitle'), value: 'workspace', slot: 'workspace' },
            ]"
            :ui="{
              list: 'memory-tabs-list overflow-x-auto overflow-y-hidden flex-nowrap',
              trigger: 'shrink-0',
            }"
            :aria-label="t('mxActivity')"
          >
            <template #commitments><MemoryCommitments :scenes="scenes" /></template>
            <template #experience><MemoryExperience :scenes="scenes" /></template>
            <template #workspace><MemoryWorkspace :scenes="scenes" /></template>
          </UTabs>
        </div>
      </template>
    </UTabs>
    <UModal v-model:open="helpOpen" :title="t('mxGuide')"
      ><template #body
        ><div class="space-y-3 text-sm text-muted">
          <p>{{ t('memoryLearningHelp') }}</p>
          <p>{{ t('memoryHelp') }}</p>
          <p>{{ t('mpTimelineHelp') }}</p>
        </div></template
      ></UModal
    >
    <UModal
      :open="modalOpen"
      :title="modalTitle"
      :description="modalDescription"
      :dismissible="!busy"
      :close="!busy"
      :ui="{
        content: 'max-w-[720px] max-h-[calc(100dvh-3rem)]',
        header: 'p-4 sm:p-6 shrink-0',
        body: 'p-4 sm:p-6 min-h-0 overflow-y-auto',
        footer: 'p-4 sm:p-6 shrink-0 justify-between flex-wrap',
      }"
      @update:open="requestClose"
    >
      <template #body>
        <div v-if="recoveryRequired || editorUncertain" class="space-y-3 mb-4">
          <UAlert
            color="warning"
            :title="t('mrUnknownTitle')"
            :description="t(recoveryRequired ? 'mrUnknownHelp' : 'mrReloadedHelp')"
          /><UButton
            v-if="recoveryRequired"
            variant="outline"
            color="neutral"
            :loading="busy"
            :disabled="busy"
            @click="load(true)"
            >{{ t('mrReload') }}</UButton
          >
        </div>
        <UAlert v-if="error" color="error" :title="error" class="mb-4" />
        <div v-if="mode === 'view' && selected" class="space-y-5">
          <div class="flex flex-wrap items-center gap-2">
            <UBadge color="neutral" variant="subtle">{{
              t(`memoryStatus_${selected.status}`)
            }}</UBadge
            ><UBadge
              v-if="selected.locked"
              color="neutral"
              variant="outline"
              icon="i-lucide-lock-keyhole"
              >{{ t('mmLocked') }}</UBadge
            ><span
              v-if="selected.scope.kind === 'scene' && selected.scope.personId"
              class="text-sm text-muted break-all"
              >{{ t('memoryPerson') }}: {{ selected.scope.personId }}</span
            >
          </div>
          <UAlert
            v-if="selected.lineageStatus && selected.lineageStatus !== 'verified'"
            color="warning"
            :title="t(`memoryLineage_${selected.lineageStatus}`)"
            :description="t('memoryLineageHelp')"
          />
          <p class="whitespace-pre-wrap break-words text-sm leading-7">{{ selected.content }}</p>
          <MemoryAssertionHistory
            v-if="selected.scope.kind === 'scene'"
            :scene-id="selected.scope.sceneId"
            :memory-id="selected.id"
          />
          <UButton
            v-if="selected.lineageStatus && selected.lineageStatus !== 'verified'"
            :disabled="busy || recoveryRequired || editorUncertain"
            color="warning"
            variant="outline"
            @click="ask('revalidate')"
            >{{ t('memoryRevalidate') }}</UButton
          >
          <dl class="grid grid-cols-2 gap-x-6 gap-y-4 border-t border-default pt-5 text-sm">
            <div>
              <dt class="text-xs text-muted">{{ t('memoryKind') }}</dt>
              <dd class="mt-1">{{ t(`memoryKind_${selected.kind}`) }}</dd>
            </div>
            <div>
              <dt class="text-xs text-muted">{{ t('memoryImportance') }}</dt>
              <dd class="mt-1">{{ t(`memoryImportance_${selected.importance}`) }}</dd>
            </div>
            <div>
              <dt class="text-xs text-muted">{{ t('memoryConfidence') }}</dt>
              <dd class="mt-1">{{ Math.round(selected.confidence * 100) }}%</dd>
            </div>
            <div>
              <dt class="text-xs text-muted">{{ t('mmOrigin') }}</dt>
              <dd class="mt-1">{{ t(`memoryOrigin_${selected.origin}`) }}</dd>
            </div>
            <div class="col-span-2">
              <dt class="text-xs text-muted">{{ t('mmLearnedAt') }}</dt>
              <dd class="mt-1">{{ date(selected.learnedAt) }}</dd>
            </div>
            <div
              v-for="entry in [
                { key: 'mpObservedTime', value: selected.observedAt },
                { key: 'mpOccurred', value: selected.occurredAt },
                { key: 'mpValidFrom', value: selected.validFrom },
                { key: 'mpValidUntil', value: selected.validUntil },
              ]"
              :key="entry.key"
            >
              <dt class="text-xs text-muted">{{ t(entry.key) }}</dt>
              <dd class="mt-1">{{ entry.value ? date(entry.value) : t('mpUnknownTime') }}</dd>
            </div>
          </dl>
          <p class="text-xs leading-5 text-muted">
            {{ t('mmConfidenceEvidence') }} {{ t('mmLockHelp') }}
          </p>
          <details class="border-t border-default pt-4">
            <summary class="text-sm cursor-pointer">
              {{ t('memorySources') }} · {{ selected.sources.length }}
            </summary>
            <p class="text-xs text-muted mt-3 leading-5">{{ t('memorySourceWarning') }}</p>
            <ul class="mt-3 space-y-3 text-xs">
              <li
                v-for="source in selected.sources"
                :key="`${source.sceneId}:${source.messageId}`"
                class="break-all"
              >
                <span class="text-muted">{{ t('scSceneId') }}:</span> {{ source.sceneId
                }}<br /><span class="text-muted">{{ t('mmMessageId') }}:</span> {{ source.messageId
                }}<br /><span class="text-muted">{{ t('memoryPerson') }}:</span>
                {{ source.senderId }}
              </li>
            </ul>
            <p v-if="!selected.sources.length" class="mt-3 text-xs text-muted">
              {{ t('mmNoSources') }}
            </p>
          </details>
          <details class="border-t border-default pt-4">
            <summary class="text-xs cursor-pointer text-muted">
              {{ t('mcInternalDetails') }}
            </summary>
            <p class="font-mono text-xs break-all mt-3">{{ selected.id }}</p>
          </details>
        </div>
        <div v-else-if="mode === 'view'" class="py-6 space-y-2">
          <h2 class="font-medium">{{ t('mmUnavailableTitle') }}</h2>
          <p class="text-sm text-muted">{{ t('mmUnavailableHelp') }}</p>
        </div>
        <form v-else id="memory-editor-form" class="space-y-5" @submit.prevent="save">
          <fieldset :disabled="busy" class="space-y-5">
            <template v-if="mode === 'new'">
              <UFormField :label="t('memoryScope')" required
                ><USelect
                  v-model="draft.scope"
                  :items="[
                    { label: t('memoryScene'), value: 'scene' },
                    { label: t('memoryPublic'), value: 'public' },
                  ]"
                  class="w-full"
              /></UFormField>
              <UFormField v-if="draft.scope === 'scene'" :label="t('scConversation')" required
                ><USelect v-model="draft.sceneId" :items="sceneOptions" class="w-full" />
                <p v-if="!sceneOptions.length" class="text-xs text-muted mt-2">
                  {{ t('mmNoScenes') }}
                </p></UFormField
              >
              <UFormField
                v-if="draft.scope === 'scene'"
                :label="t('memoryPerson')"
                :help="t('memoryPersonHelp')"
                ><UInput v-model="draft.personId" class="w-full"
              /></UFormField>
              <div v-if="draft.scope === 'public'" class="space-y-3">
                <UAlert
                  color="warning"
                  variant="subtle"
                  :title="t('memoryPublicWarning')"
                /><UCheckbox
                  id="memory-public-confirmation"
                  v-model="publicConfirmed"
                  :label="t('memoryPublicConfirm')"
                  :ui="{
                    root: 'items-center',
                    container: 'min-h-11 sm:min-h-0',
                    label: 'min-h-11 flex items-center sm:min-h-0',
                  }"
                />
              </div>
            </template>
            <div
              v-else-if="selected"
              class="rounded-md border border-default p-3 text-sm space-y-1"
            >
              <p class="font-medium">{{ t('memoryScope') }} · {{ scopeLabel(selected) }}</p>
              <p
                v-if="selected.scope.kind === 'scene' && selected.scope.personId"
                class="break-all text-muted"
              >
                {{ t('memoryPerson') }}: {{ selected.scope.personId }}
              </p>
              <p class="text-xs text-muted">{{ t('mmImmutableScope') }}</p>
            </div>
            <UFormField :label="t('memoryContent')" required
              ><UTextarea
                v-model="draft.content"
                :rows="6"
                maxlength="10000"
                class="w-full"
                required
            /></UFormField>
            <UFormField v-if="mode === 'edit'" :label="t('memoryStatus')" :help="t('mmStatusHelp')"
              ><USelect
                v-model="draft.status"
                :items="
                  ['active', 'disputed', 'superseded', 'invalidated'].map((value) => ({
                    label: t(`memoryStatus_${value}`),
                    value,
                  }))
                "
                class="w-full"
            /></UFormField>
            <USwitch
              v-model="draft.locked"
              :label="t('memoryLocked')"
              :description="t('mmLockHelp')"
            />
            <details v-if="mode === 'new'" class="border-t border-default pt-4">
              <summary class="text-sm font-medium cursor-pointer">{{ t('mmMetadata') }}</summary>
              <div class="grid sm:grid-cols-2 gap-4 mt-4">
                <UFormField :label="t('memoryKind')"
                  ><USelect
                    v-model="draft.kind"
                    :items="
                      ['fact', 'claim', 'term', 'experience', 'summary'].map((value) => ({
                        label: t(`memoryKind_${value}`),
                        value,
                      }))
                    "
                    class="w-full" /></UFormField
                ><UFormField :label="t('memoryImportance')"
                  ><USelect
                    v-model="draft.importance"
                    :items="
                      ['low', 'medium', 'high'].map((value) => ({
                        label: t(`memoryImportance_${value}`),
                        value,
                      }))
                    "
                    class="w-full" /></UFormField
                ><UFormField
                  :label="t('memoryConfidence')"
                  :help="t('mmConfidenceHelp')"
                  class="sm:col-span-2"
                  ><UInput
                    v-model.number="draft.confidence"
                    type="number"
                    min="0"
                    max="1"
                    step="any"
                    class="w-full"
                    required
                /></UFormField>
              </div>
            </details>
          </fieldset>
        </form>
      </template>
      <template #footer>
        <template v-if="mode === 'view' && selected"
          ><div class="flex flex-wrap gap-2">
            <UButton
              variant="ghost"
              color="error"
              :disabled="busy || recoveryRequired || editorUncertain"
              @click="ask('delete')"
              >{{ t('remove') }}</UButton
            ><UButton
              variant="ghost"
              color="neutral"
              :icon="selected.locked ? 'i-lucide-lock-open' : 'i-lucide-lock-keyhole'"
              :disabled="busy || recoveryRequired || editorUncertain"
              @click="ask('lock')"
              >{{ t(selected.locked ? 'memoryUnlock' : 'memoryLock') }}</UButton
            >
          </div>
          <UButton
            :disabled="busy || recoveryRequired || editorUncertain"
            @click="begin(selected)"
            >{{ t('memoryEdit') }}</UButton
          ></template
        >
        <template v-else-if="mode === 'view'">
          <div />
          <UButton @click="requestClose()">{{ t('scClose') }}</UButton>
        </template>
        <template v-else
          ><div />
          <div class="flex gap-3">
            <UButton variant="outline" color="neutral" :disabled="busy" @click="requestClose()">{{
              t('cancel')
            }}</UButton
            ><UButton
              type="submit"
              form="memory-editor-form"
              :loading="busy"
              :disabled="
                busy ||
                recoveryRequired ||
                editorUncertain ||
                !canSave ||
                (mode === 'edit' && !dirty)
              "
              >{{ t('mmSave') }}</UButton
            >
          </div></template
        >
      </template>
    </UModal>
    <UModal
      :open="!!confirmation"
      :title="confirmationCopy.title"
      :description="confirmationCopy.description"
      :dismissible="!busy"
      :close="!busy"
      @update:open="
        (open) => {
          if (!open) cancelConfirmation()
        }
      "
    >
      <template #body>
        <div
          v-if="confirmationKind !== 'discard' && (recoveryRequired || editorUncertain)"
          class="space-y-3 mb-4"
        >
          <UAlert
            color="warning"
            :title="t('mrUnknownTitle')"
            :description="t(recoveryRequired ? 'mrUnknownHelp' : 'mrReloadedHelp')"
          /><UButton
            v-if="recoveryRequired"
            variant="outline"
            color="neutral"
            :loading="busy"
            :disabled="busy"
            @click="load(true)"
            >{{ t('mrReload') }}</UButton
          >
        </div>
        <UAlert v-if="confirmationError" color="error" :title="confirmationError" />
        <p
          v-if="confirmationKind !== 'discard' && confirmationPreview"
          class="text-sm leading-6 line-clamp-3 whitespace-pre-line break-words"
        >
          {{ confirmationPreview }}
        </p></template
      >
      <template #footer
        ><div class="flex justify-end gap-3 w-full">
          <UButton
            variant="outline"
            color="neutral"
            :disabled="busy || !confirmation"
            @click="cancelConfirmation"
            >{{ t(confirmationKind === 'discard' ? 'mcKeepEditing' : 'cancel') }}</UButton
          ><UButton
            :color="
              confirmationKind === 'delete' || confirmationKind === 'discard' ? 'error' : 'primary'
            "
            :loading="busy"
            :disabled="
              busy ||
              !confirmation ||
              (confirmationKind !== 'discard' && (recoveryRequired || editorUncertain))
            "
            @click="confirmAction"
            >{{ confirmationCopy.action }}</UButton
          >
        </div></template
      >
    </UModal>
  </div>
</template>

<style scoped>
:deep(.memory-tabs-list) {
  scrollbar-width: none;
}
:deep(.memory-tabs-list::-webkit-scrollbar) {
  display: none;
}
.memory-grid {
  grid-template-columns: minmax(0, 1fr) 9rem 4.5rem 6rem 5rem 2rem;
  column-gap: 1rem;
}
.memory-tool-panel {
  min-width: 0;
}
.memory-tool-panel :deep(> section) {
  margin-top: 0;
  background: var(--ui-bg);
}
@media (max-width: 767px) {
  .memory-grid {
    grid-template-columns: minmax(0, 1fr) auto auto;
    column-gap: 0.75rem;
  }
  .memory-grid > :first-child {
    grid-column: 1 / -1;
  }
  .memory-grid > :nth-child(4) {
    grid-column: 2;
    grid-row: 3;
  }
  .memory-grid > :nth-child(5) {
    grid-column: 2;
    grid-row: 2;
  }
  .memory-grid > :nth-child(3) {
    grid-column: 1;
    grid-row: 3;
  }
  .memory-view-button {
    grid-column: 3;
    grid-row: 2 / 4;
  }
}
</style>
