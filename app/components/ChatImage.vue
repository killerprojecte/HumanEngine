<script setup lang="ts">
import { chatMediaUrl, chatImageTypes, chatImageByteLimit } from '~/utils/chatMedia'
const props = defineProps<{ mediaUrl?: string; mediaKind?: 'image' | 'sticker' }>()
const { t } = useI18n()
const { authenticatedFetch, authenticated } = useApi()
const source = ref('')
const loading = ref(false)
const decoded = ref(false)
const failure = ref<'unavailable' | 'expired' | 'failed' | ''>('')
const expanded = ref(false)
const scale = ref(1)
const viewport = ref<HTMLElement>()
const trigger = ref<HTMLButtonElement>()
let controller: AbortController | undefined
let generation = 0
let historyMarker = ''
const label = computed(() => t(props.mediaKind === 'sticker' ? 'ciSticker' : 'ciImage'))
function release() {
  controller?.abort()
  controller = undefined
  generation++
  if (source.value) URL.revokeObjectURL(source.value)
  source.value = ''
  decoded.value = false
  loading.value = false
}
async function load() {
  release()
  const url = chatMediaUrl(props.mediaUrl)
  if (!url) {
    failure.value = 'unavailable'
    loading.value = false
    return
  }
  const current = generation
  controller = new AbortController()
  loading.value = true
  failure.value = ''
  const timeout = window.setTimeout(() => {
    if (current === generation) {
      controller?.abort()
      failure.value = 'failed'
      loading.value = false
    }
  }, 20000)
  try {
    const response = await authenticatedFetch(url, { signal: controller.signal, cache: 'no-store' })
    if (!response.ok) {
      if (current === generation)
        failure.value =
          response.status === 410 ? 'expired' : response.status === 404 ? 'unavailable' : 'failed'
      return
    }
    const mime = response.headers.get('Content-Type')?.split(';')[0]?.toLowerCase() ?? ''
    if (!chatImageTypes.has(mime)) throw new Error('Unsupported image')
    const blob = await response.blob()
    if (!blob.size || blob.size > chatImageByteLimit) throw new Error('Invalid image size')
    if (current === generation) source.value = URL.createObjectURL(blob)
  } catch {
    if (current === generation && !controller?.signal.aborted) failure.value = 'failed'
  } finally {
    window.clearTimeout(timeout)
    if (current === generation) loading.value = false
  }
}
function imageLoaded(event: Event) {
  if ((event.target as HTMLImageElement).src === source.value) decoded.value = true
}
function imageError(event: Event) {
  if ((event.target as HTMLImageElement).src !== source.value) return
  release()
  failure.value = 'failed'
  loading.value = false
}
function open() {
  if (!source.value || !decoded.value || failure.value) return
  scale.value = 1
  historyMarker ||= `chat-image-${Date.now()}-${Math.random()}`
  if (history.state?.chatImagePreview !== historyMarker)
    history.pushState({ ...history.state, chatImagePreview: historyMarker }, '', location.href)
  expanded.value = true
}
function close() {
  if (!expanded.value) return
  expanded.value = false
  if (historyMarker && history.state?.chatImagePreview === historyMarker) history.back()
  nextTick(() => trigger.value?.focus())
}
function popped() {
  const shouldOpen =
    !!historyMarker &&
    history.state?.chatImagePreview === historyMarker &&
    !!source.value &&
    decoded.value &&
    !failure.value
  expanded.value = shouldOpen
  if (!shouldOpen) nextTick(() => trigger.value?.focus())
}
function dismissForNavigation() {
  expanded.value = false
  if (historyMarker && history.state?.chatImagePreview === historyMarker) {
    const { chatImagePreview: _, ...state } = history.state
    history.replaceState(state, '', location.href)
  }
  historyMarker = ''
}
function zoom(amount: number) {
  scale.value = Math.max(1, Math.min(4, scale.value + amount))
}
function keydown(event: KeyboardEvent) {
  if (!expanded.value || event.ctrlKey || event.metaKey || event.altKey) return
  if (event.key === '+' || event.key === '=') {
    event.preventDefault()
    zoom(0.5)
  }
  if (event.key === '-') {
    event.preventDefault()
    zoom(-0.5)
  }
  if (event.key === '0') {
    event.preventDefault()
    scale.value = 1
  }
}
watch(
  () => props.mediaUrl,
  () => {
    dismissForNavigation()
    void load()
  },
)
watch(authenticated, (value) => {
  if (!value) {
    dismissForNavigation()
    release()
  } else void load()
})
onMounted(() => {
  window.addEventListener('popstate', popped)
  window.addEventListener('keydown', keydown)
  void load()
})
onBeforeUnmount(() => {
  release()
  window.removeEventListener('popstate', popped)
  window.removeEventListener('keydown', keydown)
  dismissForNavigation()
})
</script>
<template>
  <span class="block my-2 max-w-full" :aria-busy="loading || (!!source && !decoded && !failure)">
    <span
      v-if="failure"
      role="status"
      class="flex flex-wrap items-center gap-2 rounded-lg border border-default bg-muted px-3 py-4 text-sm text-muted"
    >
      <UIcon name="i-lucide-image-off" class="size-5" />
      {{
        t(
          failure === 'expired'
            ? 'ciExpired'
            : failure === 'unavailable'
              ? 'ciUnavailable'
              : 'ciFailed',
        )
      }}
      <UButton v-if="mediaUrl" size="xs" variant="outline" color="neutral" @click="load">{{
        t('ciReload')
      }}</UButton>
    </span>
    <span
      v-else
      class="relative inline-flex min-h-16 min-w-24 max-w-full items-center justify-center rounded-lg bg-muted"
    >
      <span
        v-if="loading || !decoded"
        role="status"
        class="absolute inset-0 flex items-center justify-center gap-2 p-3 text-xs text-muted"
      >
        <UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" />{{ t('ciLoading') }}
      </span>
      <button
        v-if="source"
        ref="trigger"
        type="button"
        :aria-label="t('ciOpen', { type: label })"
        :disabled="!decoded"
        class="max-w-full rounded-lg focus-visible:outline-2 focus-visible:outline-primary cursor-zoom-in"
        @click="open"
      >
        <img
          :src="source"
          :alt="label"
          :class="[mediaKind === 'sticker' ? 'max-h-40' : 'max-h-72', decoded ? '' : 'opacity-0']"
          class="max-w-full rounded-lg object-contain"
          @load="imageLoaded"
          @error="imageError"
        />
      </button>
    </span>
    <UModal
      :open="expanded"
      :title="label"
      :description="t('ciKeyboard')"
      :ui="{ content: 'max-w-[96vw] sm:max-w-5xl', body: 'p-2 sm:p-4', footer: 'justify-between' }"
      @update:open="
        (value) => {
          if (!value) close()
        }
      "
    >
      <template #body>
        <div
          ref="viewport"
          tabindex="0"
          class="h-[65dvh] overflow-auto rounded-lg bg-muted outline-none focus-visible:ring-2 focus-visible:ring-primary"
        >
          <div
            class="flex min-h-full min-w-full items-center justify-center"
            :style="{ width: `${scale * 100}%`, height: `${scale * 65}dvh` }"
          >
            <img
              v-if="source && !failure"
              :src="source"
              :alt="label"
              class="h-full w-full object-contain"
              @error="imageError"
            />
            <p v-else role="status">{{ t('ciFailed') }}</p>
          </div>
        </div>
      </template>
      <template #footer>
        <div class="flex items-center gap-2">
          <UButton
            color="neutral"
            variant="outline"
            :aria-label="t('ciZoomOut')"
            :disabled="scale <= 1"
            icon="i-lucide-minus"
            @click="zoom(-0.5)"
          />
          <UButton color="neutral" variant="ghost" @click="scale = 1"
            >{{ Math.round(scale * 100) }}%</UButton
          >
          <UButton
            color="neutral"
            variant="outline"
            :aria-label="t('ciZoomIn')"
            :disabled="scale >= 4"
            icon="i-lucide-plus"
            @click="zoom(0.5)"
          />
        </div>
        <UButton color="neutral" variant="outline" @click="close">{{ t('ciBack') }}</UButton>
      </template>
    </UModal>
  </span>
</template>
