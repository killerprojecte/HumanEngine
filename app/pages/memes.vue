<script setup lang="ts">
import type { components } from '~/types/api'
type Meme = components['schemas']['Meme']
type Scene = components['schemas']['Scene']
type Patch = {
  available?: boolean
  disabled?: boolean
  favorite?: boolean
  shared?: boolean
  privacyReviewed?: boolean
  tags?: Meme['tags']
}
const { t, locale } = useI18n(),
  { client } = useApi()
const assets = ref<Meme[]>([]),
  scenes = ref<Scene[]>([]),
  loaded = ref(false),
  busy = ref(false),
  error = ref(''),
  success = ref('')
const query = ref(''),
  scope = ref('*'),
  stateFilter = ref<'*' | 'favorite' | 'disabled' | 'available' | 'unanalyzed'>('*'),
  page = ref(1),
  pageSize = ref(24),
  density = ref('comfortable'),
  total = ref(0),
  loading = ref(false),
  jumpPage = ref(1)
const images = ref<Record<string, string>>({}),
  imageState = ref<Record<string, 'loading' | 'failed'>>({})
const categories = [
  'appearance',
  'style',
  'subject',
  'emotion',
  'intent',
  'tone',
  'intensity',
  'memeMeaning',
] as const
const tagGroups = [
  { key: 'meVisualTags', fields: ['appearance', 'style', 'subject'] as const },
  {
    key: 'meIntentTags',
    fields: ['emotion', 'intent', 'tone', 'intensity', 'memeMeaning'] as const,
  },
]
const title = (asset: Meme) => asset.tags.intent[0] || asset.tags.memeMeaning[0] || t('meUntitled')
const sceneName = (id: string) => scenes.value.find((scene) => scene.id === id)?.name || id
const status = (asset: Meme) =>
  asset.disabled
    ? 'meDisabled'
    : !asset.analyzed
      ? 'meUnanalyzed'
      : !asset.sources.length
        ? 'meNoSourcesStatus'
        : asset.available
          ? 'meAvailable'
          : 'meNotSelected'
const pageCount = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))
const displayed = computed(() => assets.value)
const observedIds = ref(new Set<string>())
let observer: IntersectionObserver | undefined
const vPreview = {
  mounted(element: HTMLElement) {
    observer?.observe(element)
  },
  unmounted(element: HTMLElement) {
    observer?.unobserve(element)
    const next = new Set(observedIds.value)
    next.delete(element.dataset.assetId!)
    observedIds.value = next
  },
}
const originalImage = ref(''),
  originalFailed = ref(false)
let originalController: AbortController | undefined
const modalOpen = ref(false),
  selected = ref<Meme>()
const fresh = () => ({
  available: false,
  disabled: false,
  shared: false,
  favorite: false,
  tags: Object.fromEntries(categories.map((key) => [key, ''])) as Record<
    (typeof categories)[number],
    string
  >,
  ocr: '',
})
const draft = ref(fresh()),
  analysisScene = ref('')
let openingSnapshot = ''
const dirty = computed(() => modalOpen.value && JSON.stringify(draft.value) !== openingSnapshot)
const recoveryRequired = ref(false),
  editorUncertain = ref(false)
const blocked = computed(() => busy.value || recoveryRequired.value || editorUncertain.value)
const sourceScenes = computed(() =>
  [...new Set(selected.value?.sources.map((source) => source.sceneId) ?? [])].map((id) => ({
    label: sceneName(id),
    value: id,
  })),
)
const confirmation = ref<'discard' | 'share' | 'analyze'>(),
  confirmationKind = ref<'discard' | 'share' | 'analyze'>('discard')
const confirmationCopy = ref({ title: '', help: '', action: '', preview: '' }),
  confirmationError = ref('')
let pendingNavigation: ((allow: boolean) => void) | undefined
let disposed = false,
  previewGeneration = 0,
  previewController: AbortController | undefined
let listGeneration = 0,
  listController: AbortController | undefined
let searchTimer: ReturnType<typeof setTimeout> | undefined
class Rejected extends Error {}
function failResponse(response: Response, message: string): never {
  if (response.status >= 400 && response.status < 500) throw new Rejected(message)
  throw new Error(message)
}
function failure(cause: unknown, submitted: boolean) {
  if (submitted && !(cause instanceof Rejected)) {
    recoveryRequired.value = true
    editorUncertain.value = true
  }
  return cause instanceof Error ? cause.message : t('networkError')
}
async function load(reconcile = false, preserveSuccess = false) {
  if (disposed || busy.value || (modalOpen.value && !reconcile)) return
  listController?.abort()
  listController = new AbortController()
  const generation = ++listGeneration
  loading.value = true
  error.value = ''
  if (!preserveSuccess) success.value = ''
  assets.value = []
  observedIds.value = new Set()
  try {
    const [result, places] = await Promise.all([
      client.GET('/api/memes', {
        params: {
          query: {
            page: page.value,
            pageSize: pageSize.value,
            query: query.value.trim(),
            scope: scope.value,
            state: stateFilter.value,
          },
        },
        signal: listController.signal,
      }),
      scenes.value.length
        ? Promise.resolve(undefined)
        : client.GET('/api/scenes', { signal: listController.signal }),
    ])
    if (disposed || generation !== listGeneration) return
    if (!result.data || (places && !places.data))
      throw new Error(result.error?.error ?? places?.error?.error ?? t('networkError'))
    assets.value = result.data.items
    total.value = result.data.total
    page.value = result.data.page
    jumpPage.value = page.value
    if (places?.data) scenes.value = places.data
    loaded.value = true
    recoveryRequired.value = false
  } catch (cause) {
    if (!disposed && generation === listGeneration) {
      error.value = failure(cause, false)
      if (confirmation.value) confirmationError.value = error.value
    }
  } finally {
    if (!disposed && generation === listGeneration) loading.value = false
  }
}
function goPage(next: number) {
  if (busy.value || modalOpen.value) return
  page.value = Math.max(1, Math.min(pageCount.value, Math.floor(next) || 1))
  void load()
}
function open(asset: Meme) {
  if (busy.value || recoveryRequired.value) return
  selected.value = JSON.parse(JSON.stringify(asset))
  draft.value = {
    available: asset.available,
    disabled: asset.disabled,
    shared: asset.shared,
    favorite: asset.favorite,
    tags: Object.fromEntries(
      categories.map((key) => [key, asset.tags[key].join(', ')]),
    ) as typeof draft.value.tags,
    ocr: asset.tags.ocr,
  }
  openingSnapshot = JSON.stringify(draft.value)
  analysisScene.value = asset.sources[0]?.sceneId ?? ''
  error.value = ''
  success.value = ''
  editorUncertain.value = false
  modalOpen.value = true
}
function close() {
  modalOpen.value = false
  confirmation.value = undefined
  error.value = ''
}
function ask(kind: NonNullable<typeof confirmation.value>) {
  if (busy.value || (kind !== 'discard' && blocked.value) || !selected.value) return
  confirmationKind.value = kind
  confirmationCopy.value = {
    title: t(
      kind === 'discard' ? 'mcDiscardTitle' : kind === 'share' ? 'meShareTitle' : 'meAnalyzeTitle',
    ),
    help: t(
      kind === 'discard' ? 'meDiscardHelp' : kind === 'share' ? 'meShareHelp' : 'meAnalyzeHelp',
    ),
    action: t(
      kind === 'discard' ? 'mcDiscard' : kind === 'share' ? 'meSaveShared' : 'meAnalyzeConfirm',
    ),
    preview:
      title(selected.value) + (kind === 'analyze' ? ` · ${sceneName(analysisScene.value)}` : ''),
  }
  confirmationError.value = ''
  confirmation.value = kind
}
function requestClose(value = false) {
  if (value || busy.value) return
  if (dirty.value) ask('discard')
  else close()
}
function cancelConfirmation() {
  if (busy.value) return
  confirmation.value = undefined
  pendingNavigation?.(false)
  pendingNavigation = undefined
}
function discard() {
  close()
  pendingNavigation?.(true)
  pendingNavigation = undefined
}
function patchBody(): Patch {
  if (!selected.value) return {}
  const original = selected.value,
    patch: Patch = {}
  for (const field of ['available', 'disabled', 'favorite'] as const)
    if (draft.value[field] !== original[field]) patch[field] = draft.value[field]
  if (draft.value.shared !== original.shared) {
    patch.shared = draft.value.shared
    patch.privacyReviewed = draft.value.shared
  }
  const tags = { ...original.tags }
  for (const category of categories)
    if (draft.value.tags[category] !== original.tags[category].join(', '))
      tags[category] = draft.value.tags[category]
        .split(',')
        .map((tag) => tag.trim())
        .filter(Boolean)
  tags.ocr = draft.value.ocr
  if (JSON.stringify(tags) !== JSON.stringify(original.tags)) patch.tags = tags
  return patch
}
async function save(shareConfirmed = false) {
  if (blocked.value || !selected.value || !dirty.value) return
  const patch = patchBody(),
    original = selected.value
  if (!Object.keys(patch).length) {
    close()
    return
  }
  if (patch.shared === true && !shareConfirmed) {
    ask('share')
    return
  }
  let submitted = false
  busy.value = true
  error.value = ''
  confirmationError.value = ''
  try {
    const latest = await client.GET('/api/memes/{id}', { params: { path: { id: original.id } } })
    if (!latest.data) throw new Error(latest.error?.error ?? t('networkError'))
    const current = latest.data
    if (
      !current ||
      Object.keys(patch).some(
        (key) =>
          JSON.stringify(current[key as keyof Meme]) !==
          JSON.stringify(original[key as keyof Meme]),
      ) ||
      (patch.tags && current.tagRevision !== original.tagRevision)
    )
      throw new Error(t('mcConfigChanged'))
    submitted = true
    const result = await client.PATCH('/api/memes/{id}', {
      params: { path: { id: original.id } },
      body: patch,
    })
    if (!result.data) failResponse(result.response, result.error?.error ?? t('networkError'))
    assets.value = assets.value.map((asset) => (asset.id === original.id ? result.data! : asset))
    close()
    success.value = t('meSaved')
  } catch (cause) {
    error.value = failure(cause, submitted)
    if (confirmation.value) confirmationError.value = error.value
  } finally {
    busy.value = false
    if (!modalOpen.value) await load(false, true)
  }
}
async function analyze() {
  if (
    blocked.value ||
    dirty.value ||
    !selected.value ||
    !analysisScene.value ||
    selected.value.disabled ||
    !sourceScenes.value.some((source) => source.value === analysisScene.value)
  )
    return
  const original = selected.value,
    sceneId = analysisScene.value
  let submitted = false
  busy.value = true
  confirmationError.value = ''
  error.value = ''
  try {
    const latest = await client.GET('/api/memes/{id}', { params: { path: { id: original.id } } })
    if (!latest.data) throw new Error(latest.error?.error ?? t('networkError'))
    const current = latest.data
    if (
      !current ||
      current.disabled ||
      !current.sources.some((source) => source.sceneId === sceneId) ||
      JSON.stringify(current.tags) !== JSON.stringify(original.tags) ||
      current.tagRevision !== original.tagRevision
    )
      throw new Error(t('mcConfigChanged'))
    submitted = true
    const result = await client.POST('/api/memes/{id}/analyze', {
      params: { path: { id: original.id } },
      body: { sceneId },
    })
    if (!result.data) failResponse(result.response, result.error?.error ?? t('networkError'))
    // Analysis responses can sanitize provenance; reread administrative metadata instead of replacing it with a projection.
    const refreshed = await client.GET('/api/memes/{id}', { params: { path: { id: original.id } } })
    if (!refreshed.data) throw new Error(refreshed.error?.error ?? t('networkError'))
    assets.value = assets.value.map((asset) => (asset.id === original.id ? refreshed.data! : asset))
    close()
    success.value = t('meAnalyzed')
  } catch (cause) {
    confirmationError.value = failure(cause, submitted)
  } finally {
    busy.value = false
    if (!modalOpen.value) await load(false, true)
  }
}
async function confirm() {
  if (busy.value || !confirmation.value) return
  if (confirmation.value === 'discard') discard()
  else if (confirmation.value === 'share') await save(true)
  else await analyze()
}
const wantedPreviewIds = computed(() =>
  displayed.value.filter((asset) => observedIds.value.has(asset.id)).map((asset) => asset.id),
)
async function previews(ids: string[]) {
  previewController?.abort()
  const generation = ++previewGeneration
  previewController = new AbortController()
  const signal = previewController.signal
  for (const id of Object.keys(images.value))
    if (!ids.includes(id)) {
      URL.revokeObjectURL(images.value[id]!)
      delete images.value[id]
    }
  for (const id of Object.keys(imageState.value)) if (!ids.includes(id)) delete imageState.value[id]
  const queue = ids.filter((id) => !images.value[id])
  await Promise.all(
    Array.from({ length: Math.min(3, queue.length) }, async () => {
      while (queue.length && !disposed && generation === previewGeneration) {
        const id = queue.shift()!
        imageState.value[id] = 'loading'
        try {
          const result = await client.GET('/api/memes/{id}/thumbnail', {
            params: { path: { id } },
            parseAs: 'blob',
            signal,
          })
          if (disposed || generation !== previewGeneration) return
          if (!result.data) {
            imageState.value[id] = 'failed'
            continue
          }
          images.value[id] = URL.createObjectURL(result.data)
          delete imageState.value[id]
        } catch {
          if (!disposed && generation === previewGeneration) imageState.value[id] = 'failed'
        }
      }
    }),
  )
}
function imageFailed(id: string) {
  if (images.value[id]) URL.revokeObjectURL(images.value[id]!)
  delete images.value[id]
  imageState.value[id] = 'failed'
}
watch(wantedPreviewIds, (ids) => {
  if (import.meta.client) void previews(ids)
})
watch([scope, stateFilter, pageSize], () => {
  page.value = 1
  void load()
})
watch(query, () => {
  clearTimeout(searchTimer)
  listController?.abort()
  ++listGeneration
  assets.value = []
  loading.value = true
  searchTimer = setTimeout(() => {
    page.value = 1
    void load()
  }, 250)
})
function failOriginal() {
  if (originalImage.value) URL.revokeObjectURL(originalImage.value)
  originalImage.value = ''
  originalFailed.value = true
}
async function loadOriginal() {
  originalController?.abort()
  if (originalImage.value) URL.revokeObjectURL(originalImage.value)
  originalImage.value = ''
  originalFailed.value = false
  if (!modalOpen.value || !selected.value) return
  const controller = new AbortController(),
    id = selected.value.id
  originalController = controller
  try {
    const result = await client.GET('/api/memes/{id}/file', {
      params: { path: { id } },
      parseAs: 'blob',
      signal: controller.signal,
    })
    if (disposed || controller.signal.aborted) return
    if (!result.data) throw new Error('preview_failed')
    originalImage.value = URL.createObjectURL(result.data)
  } catch {
    if (!controller.signal.aborted) originalFailed.value = true
  }
}
watch(modalOpen, () => {
  void loadOriginal()
})
onBeforeRouteLeave(() => {
  if (busy.value) return false
  if (!dirty.value) return true
  ask('discard')
  return new Promise<boolean>((resolve) => {
    pendingNavigation?.(false)
    pendingNavigation = resolve
  })
})
function beforeUnload(event: BeforeUnloadEvent) {
  if (dirty.value || busy.value) {
    event.preventDefault()
    event.returnValue = ''
  }
}
onMounted(() => {
  observer = new IntersectionObserver(
    (entries) => {
      const next = new Set(observedIds.value)
      for (const entry of entries)
        if (entry.isIntersecting) next.add((entry.target as HTMLElement).dataset.assetId!)
      observedIds.value = next
    },
    { rootMargin: '200px' },
  )
  void load()
  window.addEventListener('beforeunload', beforeUnload)
})
onBeforeUnmount(() => {
  disposed = true
  ++previewGeneration
  ++listGeneration
  clearTimeout(searchTimer)
  listController?.abort()
  originalController?.abort()
  observer?.disconnect()
  if (originalImage.value) URL.revokeObjectURL(originalImage.value)
  previewController?.abort()
  Object.values(images.value).forEach((url) => URL.revokeObjectURL(url))
  pendingNavigation?.(false)
  window.removeEventListener('beforeunload', beforeUnload)
})
const date = (at: string) => new Date(at).toLocaleString(locale.value)
</script>
<template>
  <div class="space-y-5">
    <header class="flex items-start justify-between gap-4">
      <div>
        <h1 class="page-heading">{{ t('meTitle') }}</h1>
        <p class="mt-2 text-sm text-muted">{{ t('meIntro') }}</p>
      </div>
      <UButton
        class="shrink-0 whitespace-nowrap"
        icon="i-lucide-refresh-cw"
        color="neutral"
        variant="outline"
        :loading="loading"
        :disabled="busy || modalOpen"
        @click="load()"
        >{{ t('refresh') }}</UButton
      >
    </header>
    <UAlert v-if="error && !modalOpen" color="error" :title="error" /><UAlert
      v-if="success"
      color="success"
      variant="subtle"
      :title="success"
    />
    <div v-if="recoveryRequired && !modalOpen" class="space-y-3">
      <UAlert
        color="warning"
        :title="t('mrUnknownTitle')"
        :description="t('meRecoveryHelp')"
      /><UButton :disabled="busy" @click="load(true)">{{ t('mrReload') }}</UButton>
    </div>
    <section class="surface overflow-hidden">
      <div class="flex flex-wrap gap-3 p-4">
        <UInput
          v-model="query"
          icon="i-lucide-search"
          :placeholder="t('meSearch')"
          :aria-label="t('meSearch')"
          class="w-full sm:w-64"
        /><USelect
          v-model="scope"
          :aria-label="t('memoryScope')"
          :items="[
            { label: t('mmAllScopes'), value: '*' },
            { label: t('shared'), value: 'shared' },
            { label: t('sceneOnly'), value: 'local' },
            ...scenes.map((scene) => ({ label: scene.name, value: 'scene:' + scene.id })),
          ]"
          class="w-full sm:w-48"
        /><USelect
          v-model="stateFilter"
          :aria-label="t('meStateFilter')"
          :items="[
            { label: t('meAllStates'), value: '*' },
            { label: t('meAvailable'), value: 'available' },
            { label: t('meDisabled'), value: 'disabled' },
            { label: t('favorite'), value: 'favorite' },
            { label: t('meUnanalyzed'), value: 'unanalyzed' },
          ]"
          class="w-full sm:w-44"
        />
      </div>
      <div class="flex flex-wrap items-end gap-3 px-4 pb-4">
        <UFormField :label="t('mePageSize')"
          ><USelect
            v-model="pageSize"
            :items="[12, 24, 48, 96]"
            class="w-28"
            :disabled="busy || modalOpen"
        /></UFormField>
        <UFormField :label="t('meDensity')"
          ><USelect
            v-model="density"
            :items="[
              { label: t('meComfortable'), value: 'comfortable' },
              { label: t('meCompact'), value: 'compact' },
            ]"
            class="w-36"
        /></UFormField>
        <p class="text-xs text-muted pb-2">{{ t('meStaticPreview') }}</p>
      </div>
      <p class="px-4 pb-4 text-xs text-muted leading-5">{{ t('memePrivacyHelp') }}</p>
    </section>
    <p v-if="loading" class="p-10 text-center text-muted">{{ t('uxLoading') }}</p>
    <p v-else-if="loaded && !assets.length && !error" class="p-10 text-center text-muted">
      {{ t(query || scope !== '*' || stateFilter !== '*' ? 'uxNoMatches' : 'emptyMemes') }}
    </p>
    <div
      class="grid gap-4"
      :class="
        density === 'compact'
          ? 'grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 2xl:grid-cols-6'
          : 'grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4'
      "
      :aria-busy="loading"
    >
      <article
        v-for="asset in displayed"
        :key="asset.id"
        v-preview
        :data-asset-id="asset.id"
        class="surface overflow-hidden flex flex-col"
      >
        <div
          class="bg-elevated/60 flex items-center justify-center p-3"
          :class="density === 'compact' ? 'h-24' : 'h-36'"
        >
          <img
            v-if="images[asset.id]"
            :src="images[asset.id]"
            :alt="title(asset)"
            class="max-h-full max-w-full object-contain"
            @error="imageFailed(asset.id)"
          />
          <p v-else class="text-xs text-muted text-center">
            {{ t(imageState[asset.id] === 'failed' ? 'mePreviewFailed' : 'mePreviewLoading') }}
          </p>
        </div>
        <div class="p-3 flex flex-col gap-2 flex-1">
          <div class="flex justify-between gap-3">
            <h2 class="text-sm font-medium break-words line-clamp-2">{{ title(asset) }}</h2>
            <UIcon
              v-if="asset.favorite"
              name="i-lucide-star"
              class="size-4 shrink-0 text-warning"
              :aria-label="t('favorite')"
            />
          </div>
          <div class="flex flex-wrap items-center gap-2 text-xs">
            <UBadge color="neutral" variant="subtle">{{ t(status(asset)) }}</UBadge
            ><span class="text-muted">{{ t(asset.shared ? 'shared' : 'sceneOnly') }}</span>
          </div>
          <p class="text-xs text-muted">
            {{ asset.format.toUpperCase() }} · {{ asset.frames }} {{ t('frames') }} ·
            {{ asset.width }} × {{ asset.height }}
          </p>
          <p class="text-xs text-muted break-words line-clamp-2">
            {{
              [...new Set([...asset.tags.intent, ...asset.tags.style])].slice(0, 4).join(' · ') ||
              t('meNoTags')
            }}
          </p>
          <div class="mt-auto flex justify-end">
            <UButton
              variant="ghost"
              color="neutral"
              :disabled="busy || recoveryRequired"
              @click="open(asset)"
              >{{ t('meDetails') }}</UButton
            >
          </div>
        </div>
      </article>
    </div>
    <footer v-if="total && !loading" class="flex flex-wrap justify-between items-center gap-3">
      <p class="text-sm text-muted">
        {{ t('mePage', { page, pages: pageCount, total }) }}
      </p>
      <div class="flex flex-wrap items-center gap-2">
        <UInput
          v-model.number="jumpPage"
          type="number"
          :min="1"
          :max="pageCount"
          :aria-label="t('meJumpPage')"
          class="w-20"
          @keydown.enter="goPage(jumpPage)"
        />
        <UButton color="neutral" variant="ghost" @click="goPage(jumpPage)">{{
          t('meJumpPage')
        }}</UButton>
        <UButton
          color="neutral"
          variant="outline"
          :disabled="page <= 1"
          @click="goPage(page - 1)"
          >{{ t('mePrevious') }}</UButton
        ><UButton
          color="neutral"
          variant="outline"
          :disabled="page >= pageCount"
          @click="goPage(page + 1)"
          >{{ t('meNext') }}</UButton
        >
      </div>
    </footer>
    <UModal
      :open="modalOpen"
      :title="t('meDetails')"
      :description="selected ? title(selected) : ''"
      :dismissible="!busy"
      :close="!busy"
      :ui="{
        content: 'max-w-[760px] max-h-[calc(100dvh-3rem)]',
        header: 'p-4 sm:p-6 shrink-0',
        body: 'p-4 sm:p-6 min-h-0 overflow-y-auto',
        footer: 'p-4 sm:p-6 shrink-0 justify-end',
      }"
      @update:open="requestClose"
    >
      <template #body
        ><div v-if="recoveryRequired || editorUncertain" class="space-y-3 mb-4">
          <UAlert
            color="warning"
            :title="t('mrUnknownTitle')"
            :description="t(recoveryRequired ? 'meRecoveryHelp' : 'meRecoveredHelp')"
          /><UButton v-if="recoveryRequired" :disabled="busy" @click="load(true)">{{
            t('mrReload')
          }}</UButton>
        </div>
        <UAlert v-if="error" color="error" :title="error" class="mb-4" />
        <form v-if="selected" id="meme-editor-form" class="space-y-5" @submit.prevent="save()">
          <div class="h-48 bg-elevated/60 rounded-md flex items-center justify-center p-3">
            <img
              v-if="originalImage"
              :src="originalImage"
              :alt="title(selected)"
              class="max-h-full max-w-full object-contain"
              @error="failOriginal()"
            />
            <div v-else class="text-center space-y-2">
              <p class="text-sm text-muted">
                {{ t(originalFailed ? 'mePreviewFailed' : 'mePreviewLoading') }}
              </p>
              <UButton
                v-if="originalFailed"
                type="button"
                variant="ghost"
                @click="loadOriginal()"
                >{{ t('meRetryPreview') }}</UButton
              >
            </div>
          </div>
          <p class="text-xs text-muted">
            {{ selected.format.toUpperCase() }} · {{ selected.width }} × {{ selected.height }} ·
            {{ selected.frames }} {{ t('frames') }} · {{ Math.round(selected.bytes / 1024) }} KiB
          </p>
          <UAlert
            v-if="selected.analysisError"
            color="warning"
            :title="t('meAnalysisError')"
            :description="selected.analysisError"
          />
          <fieldset :disabled="blocked" class="space-y-5">
            <section class="space-y-4">
              <h3 class="font-medium text-sm">{{ t('meUseAndScope') }}</h3>
              <p class="text-xs text-muted leading-5">{{ t('meDraftHelp') }}</p>
              <USwitch
                v-model="draft.disabled"
                :label="t('meDisableLabel')"
                :description="t('meDisableHelp')"
              /><USwitch
                v-model="draft.available"
                :disabled="draft.disabled"
                :label="t('meAvailableLabel')"
                :description="t('meAvailableHelp')"
              />
              <p class="text-xs text-muted">
                {{ t('meSelectionOrigin') }}:
                {{
                  t(
                    selected.selectionOrigin === 'manual'
                      ? 'meManual'
                      : selected.selectionOrigin === 'automatic'
                        ? 'meAutomatic'
                        : 'meUnset',
                  )
                }}
              </p>
              <UCheckbox
                v-model="draft.shared"
                :label="t('meSharedLabel')"
                :description="t('meSharedHelp')"
              />
              <p class="text-xs text-muted">
                {{ t('meSharingOrigin') }}:
                {{
                  t(
                    selected.sharingOrigin === 'manual'
                      ? 'meManual'
                      : selected.sharingOrigin === 'automatic'
                        ? 'meAutomatic'
                        : 'meUnset',
                  )
                }}
              </p>
              <USwitch
                v-model="draft.favorite"
                :label="t('favorite')"
                :description="t('meFavoriteHelp')"
              />
            </section>
            <details class="border-t border-default pt-4">
              <summary class="cursor-pointer font-medium text-sm">{{ t('editTags') }}</summary>
              <p class="mt-3 text-xs text-muted">{{ t('commaTags') }}</p>
              <section v-for="group in tagGroups" :key="group.key" class="mt-5 space-y-4">
                <h3 class="text-xs font-medium text-muted">{{ t(group.key) }}</h3>
                <div class="grid sm:grid-cols-2 gap-4">
                  <UFormField
                    v-for="category in group.fields"
                    :key="category"
                    :label="t('meTag_' + category)"
                    ><UInput v-model="draft.tags[category]" class="w-full"
                  /></UFormField>
                </div>
              </section>
              <UFormField :label="t('meOcr')" class="mt-5"
                ><UTextarea v-model="draft.ocr" :rows="3" class="w-full"
              /></UFormField>
            </details>
          </fieldset>
          <details class="border-t border-default pt-4">
            <summary class="cursor-pointer font-medium text-sm">{{ t('meAnalysis') }}</summary>
            <div class="space-y-3 mt-4">
              <p class="text-xs text-muted leading-5">{{ t('meAnalysisHelp') }}</p>
              <p v-if="dirty" class="text-xs text-warning">{{ t('meSaveBeforeAnalysis') }}</p>
              <USelect
                v-if="sourceScenes.length"
                v-model="analysisScene"
                :items="sourceScenes"
                :disabled="blocked"
                :aria-label="t('meSourceConversation')"
                class="w-full"
              />
              <p v-else class="text-xs text-muted">{{ t('meNoAnalysisSource') }}</p>
              <UButton
                type="button"
                color="neutral"
                variant="outline"
                :disabled="blocked || dirty || selected.disabled || !sourceScenes.length"
                @click="ask('analyze')"
                >{{ t('meAnalyzeButton') }}</UButton
              >
            </div>
          </details>
          <details class="border-t border-default pt-4">
            <summary class="cursor-pointer text-sm">
              {{ t('memorySources') }} · {{ selected.sources.length }}
            </summary>
            <p class="mt-3 text-xs text-muted">{{ t('memorySourceWarning') }}</p>
            <ul class="space-y-3 mt-3">
              <li
                v-for="source in selected.sources"
                :key="JSON.stringify([source.sceneId, source.messageId])"
                class="text-xs break-all"
              >
                <p class="font-medium">{{ sceneName(source.sceneId) }}</p>
                <p>{{ t('scSceneId') }}: {{ source.sceneId }}</p>
                <p>{{ t('mmMessageId') }}: {{ source.messageId }}</p>
                <p>{{ t('memoryPerson') }}: {{ source.senderId }}</p>
              </li>
            </ul>
          </details>
          <details class="border-t border-default pt-4">
            <summary class="cursor-pointer text-xs text-muted">
              {{ t('mcInternalDetails') }}
            </summary>
            <dl class="mt-3 space-y-2 text-xs text-muted">
              <div>
                <dt>ID</dt>
                <dd class="font-mono break-all">{{ selected.id }}</dd>
              </div>
              <div>
                <dt>{{ t('meCreated') }}</dt>
                <dd>{{ date(selected.createdAt) }}</dd>
              </div>
              <div>
                <dt>{{ t('meUsageCount') }}</dt>
                <dd>{{ selected.usageCount }}</dd>
              </div>
              <div>
                <dt>{{ t('meLearnedPreference') }}</dt>
                <dd>{{ selected.preference.toFixed(2) }}</dd>
              </div>
            </dl>
          </details>
        </form> </template
      ><template #footer
        ><UButton
          color="neutral"
          variant="outline"
          :aria-label="t(dirty ? 'cancel' : 'scClose')"
          :disabled="busy"
          @click="requestClose()"
          >{{ t(dirty ? 'cancel' : 'scClose') }}</UButton
        ><UButton
          type="submit"
          form="meme-editor-form"
          :loading="busy"
          :disabled="blocked || !dirty"
          >{{ t('meSave') }}</UButton
        ></template
      >
    </UModal>
    <UModal
      :open="!!confirmation"
      :title="confirmationCopy.title"
      :description="confirmationCopy.help"
      :dismissible="!busy"
      :close="!busy"
      @update:open="
        (value) => {
          if (!value) cancelConfirmation()
        }
      "
      ><template #body
        ><div
          v-if="confirmationKind !== 'discard' && (recoveryRequired || editorUncertain)"
          class="mb-4 space-y-3"
        >
          <UAlert
            color="warning"
            :title="t('mrUnknownTitle')"
            :description="t(recoveryRequired ? 'meRecoveryHelp' : 'meRecoveredHelp')"
          /><UButton v-if="recoveryRequired" :disabled="busy" @click="load(true)">{{
            t('mrReload')
          }}</UButton>
        </div>
        <UAlert v-if="confirmationError" color="error" :title="confirmationError" />
        <p class="mt-3 text-sm break-words">{{ confirmationCopy.preview }}</p></template
      ><template #footer
        ><div class="w-full flex justify-end gap-3">
          <UButton
            color="neutral"
            variant="outline"
            :disabled="busy || !confirmation"
            @click="cancelConfirmation"
            >{{ t(confirmationKind === 'discard' ? 'mcKeepEditing' : 'cancel') }}</UButton
          ><UButton
            :color="confirmationKind === 'discard' ? 'error' : 'primary'"
            :loading="busy"
            :disabled="busy || !confirmation || (confirmationKind !== 'discard' && blocked)"
            @click="confirm"
            >{{ confirmationCopy.action }}</UButton
          >
        </div></template
      ></UModal
    >
  </div>
</template>
