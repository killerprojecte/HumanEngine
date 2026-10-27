<script setup lang="ts">
import type { components } from '~/types/api'
type Memory = components['schemas']['Memory']
type Scene = components['schemas']['Scene']
const { t, locale } = useI18n()
const { client } = useApi()
const memories = ref<Memory[]>([]),
  scenes = ref<Scene[]>([])
const error = ref(''),
  busy = ref(false),
  loaded = ref(false),
  saved = ref(false)
const sceneFilter = ref('all'),
  personFilter = ref(''),
  query = ref('')
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
      (!query.value.trim() ||
        memory.content.toLocaleLowerCase().includes(query.value.trim().toLocaleLowerCase()))
    )
  }),
)
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
        body: patch,
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
        body:
          action === 'revalidate'
            ? { content: record.content, status: 'active' }
            : { locked: !record.locked },
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
  <div class="space-y-5">
    <header class="flex items-start justify-between gap-4">
      <div>
        <h1 class="page-heading">{{ t('memoryTitle') }}</h1>
        <p class="mt-2 text-sm text-muted">{{ t('memoryHelp') }}</p>
      </div>
      <div class="flex gap-2 shrink-0">
        <UButton
          variant="ghost"
          color="neutral"
          icon="i-lucide-refresh-cw"
          :aria-label="t('refresh')"
          :disabled="busy || modalOpen"
          @click="load()"
        /><UButton
          icon="i-lucide-plus"
          :disabled="busy || !loaded || recoveryRequired"
          @click="begin()"
          >{{ t('memoryCreate') }}</UButton
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
    <section class="surface overflow-hidden">
      <div class="p-4 border-b border-default space-y-4">
        <div
          class="grid grid-cols-1 md:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)_minmax(0,1fr)] gap-4 items-start"
        >
          <UFormField :label="t('memoryScope')"
            ><USelect
              v-model="sceneFilter"
              :items="[
                { label: t('mmAllScopes'), value: 'all' },
                { label: t('memoryPublic'), value: 'public' },
                ...sceneOptions.map((scene) => ({
                  label: scene.label,
                  value: `scene:${scene.value}`,
                })),
              ]"
              class="w-full"
          /></UFormField>
          <UFormField :label="t('memoryPerson')"
            ><UInput
              v-model="personFilter"
              :disabled="!currentSceneId"
              :placeholder="t(currentSceneId ? 'memoryPersonFilter' : 'mmChooseSceneFirst')"
              class="w-full"
          /></UFormField>
          <UFormField :label="t('mmSearch')"
            ><UInput
              v-model="query"
              icon="i-lucide-search"
              :placeholder="t('memorySearch')"
              class="w-full"
          /></UFormField>
        </div>
        <div class="flex flex-wrap items-start justify-between gap-2 text-xs">
          <p class="text-muted break-words">
            <span class="font-medium text-default">{{ t('mmCurrentScope') }}</span>
            {{ activeScopeLabel
            }}<span v-if="currentSceneId && personFilter.trim()">
              · {{ t('memoryPerson') }}: {{ personFilter.trim() }}</span
            >
          </p>
          <span class="text-muted tabular shrink-0">{{
            t('mmCount', { count: visible.length, total: memories.length })
          }}</span>
        </div>
      </div>
      <div v-if="!loaded && busy" class="p-10 text-center text-muted text-sm">
        {{ t('mmLoading') }}
      </div>
      <div v-else-if="!visible.length" class="py-12 px-6 text-center space-y-2">
        <p class="font-medium">{{ t('memoryEmpty') }}</p>
        <p class="text-sm text-muted">{{ t('mmEmptyHelp') }}</p>
      </div>
      <div v-else class="divide-y divide-default">
        <article
          v-for="memory in visible"
          :key="memory.id"
          class="p-4 sm:px-5 hover:bg-elevated/30"
        >
          <div class="flex items-start gap-4">
            <div class="min-w-0 flex-1 space-y-2.5">
              <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
                <span class="font-medium text-muted break-words">{{ scopeLabel(memory) }}</span
                ><span
                  v-if="memory.scope.kind === 'scene' && memory.scope.personId"
                  class="text-muted break-all"
                  >{{ t('memoryPerson') }}: {{ memory.scope.personId }}</span
                ><UBadge
                  size="sm"
                  :color="memory.status === 'active' ? 'neutral' : 'warning'"
                  variant="subtle"
                  >{{ t(`memoryStatus_${memory.status}`) }}</UBadge
                ><UBadge
                  v-if="memory.lineageStatus && memory.lineageStatus !== 'verified'"
                  color="warning"
                  variant="subtle"
                  >{{ t(`memoryLineage_${memory.lineageStatus}`) }}</UBadge
                ><span v-if="memory.locked" class="inline-flex items-center gap-1 text-muted"
                  ><UIcon name="i-lucide-lock-keyhole" class="size-3.5" />{{ t('mmLocked') }}</span
                >
              </div>
              <p class="text-sm leading-6 whitespace-pre-line break-words line-clamp-3">
                {{ memory.content }}
              </p>
              <p class="text-xs text-muted">
                {{ t(`memoryKind_${memory.kind}`) }} · {{ t(`memoryOrigin_${memory.origin}`) }} ·
                {{ t('memoryConfidence') }} {{ Math.round(memory.confidence * 100) }}%
              </p>
            </div>
            <UButton
              variant="ghost"
              color="neutral"
              class="shrink-0"
              :aria-label="t('mmViewMemory')"
              :disabled="busy || recoveryRequired"
              @click="openDetails(memory)"
              >{{ t('mmView') }}</UButton
            >
          </div>
        </article>
      </div>
    </section>
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
