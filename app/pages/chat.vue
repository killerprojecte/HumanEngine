<script setup lang="ts">
import type { components } from '~/types/api'
const { t, locale } = useI18n(),
  { client } = useApi()
const mode = ref<'simulation' | 'real'>('simulation'),
  session = useState<string>('simulationId', () => ''),
  text = ref(''),
  pendingText = ref(''),
  busy = ref(false),
  previewLoading = ref(false),
  error = ref(''),
  coverage = ref(''),
  messages = ref<components['schemas']['Message'][]>([]),
  sceneId = ref(''),
  scenes = ref<components['schemas']['Scene'][]>([]),
  transcript = ref<HTMLDivElement>(),
  sceneSearch = ref('')
const selectedScene = computed(() => scenes.value.find((scene) => scene.id === sceneId.value))
const filteredScenes = computed(() =>
  scenes.value.filter((scene) =>
    scene.name.toLowerCase().includes(sceneSearch.value.toLowerCase()),
  ),
)
let generation = 0,
  sessionGeneration = 0,
  disposed = false
async function start() {
  const version = ++sessionGeneration
  const r = await client.POST('/api/simulations')
  if (!r.data) throw new Error(r.error?.error ?? t('networkError'))
  if (disposed || version !== sessionGeneration) {
    void client.DELETE('/api/simulations/{id}', { params: { path: { id: r.data.id } } })
    return
  }
  session.value = r.data.id
}
onMounted(async () => {
  await Promise.all([
    start().catch(() => {
      if (!disposed) error.value = t('networkError')
    }),
    client
      .GET('/api/scenes')
      .then((r) => {
        if (!disposed) {
          scenes.value = r.data ?? []
          if (r.error) error.value = r.error.error
        }
      })
      .catch(() => {
        if (!disposed) error.value = t('networkError')
      }),
  ])
})
async function clear() {
  const version = ++generation,
    old = session.value
  session.value = ''
  messages.value = []
  text.value = ''
  pendingText.value = ''
  busy.value = false
  error.value = ''
  if (old)
    try {
      await client.DELETE('/api/simulations/{id}', { params: { path: { id: old } } })
    } catch {
      if (version === generation) error.value = t('networkError')
    }
  if (version === generation && !disposed)
    try {
      await start()
    } catch {
      if (!disposed) error.value = t('networkError')
    }
}
async function send() {
  if (busy.value || !text.value.trim() || !session.value || mode.value !== 'simulation') return
  const version = ++generation,
    value = text.value
  text.value = ''
  pendingText.value = value
  busy.value = true
  error.value = ''
  try {
    const r = await client.POST('/api/simulations/{id}/messages', {
      params: { path: { id: session.value } },
      body: { text: value },
    })
    if (version !== generation || disposed) return
    if (r.data) messages.value = r.data
    else {
      error.value = r.error?.error ?? t('networkError')
      text.value = value
    }
  } catch {
    if (version === generation && !disposed) {
      error.value = t('networkError')
      text.value = value
    }
  } finally {
    if (version === generation && !disposed) {
      busy.value = false
      pendingText.value = ''
    }
  }
}
async function preview() {
  if (!sceneId.value || mode.value !== 'real') return
  const version = ++generation
  error.value = ''
  coverage.value = ''
  previewLoading.value = true
  try {
    const r = await client.GET('/api/scenes/{id}/history', {
      params: { path: { id: sceneId.value } },
    })
    if (version === generation && !disposed) {
      messages.value = r.data?.items ?? []
      coverage.value = r.data?.coverage ?? ''
      error.value = r.error?.error ?? ''
    }
  } catch {
    if (version === generation && !disposed) error.value = t('networkError')
  } finally {
    if (version === generation && !disposed) previewLoading.value = false
  }
}
function selectScene(id: string) {
  sceneId.value = id
  if (mode.value === 'real') {
    messages.value = []
    void preview()
  } else mode.value = 'real'
}
watch(mode, () => {
  const version = ++generation
  messages.value = []
  pendingText.value = ''
  busy.value = false
  previewLoading.value = false
  error.value = ''
  coverage.value = ''
  if (mode.value === 'real') void preview()
  else if (session.value)
    void client
      .GET('/api/simulations/{id}', { params: { path: { id: session.value } } })
      .then((r) => {
        if (version === generation && !disposed) {
          messages.value = r.data ?? []
          error.value = r.error?.error ?? ''
        }
      })
      .catch(() => {
        if (version === generation && !disposed) error.value = t('networkError')
      })
  else
    void start().catch(() => {
      if (!disposed) error.value = t('networkError')
    })
})
watch([messages, busy], async () => {
  await nextTick()
  transcript.value?.scrollTo({ top: transcript.value.scrollHeight, behavior: 'smooth' })
})
onBeforeUnmount(() => {
  disposed = true
  generation++
  sessionGeneration++
  if (session.value) {
    void client.DELETE('/api/simulations/{id}', { params: { path: { id: session.value } } })
    session.value = ''
  }
})
function time(value: string) {
  return new Date(value).toLocaleTimeString(locale.value, { hour: '2-digit', minute: '2-digit' })
}
</script>
<template>
  <div class="flex flex-1 min-h-0 overflow-hidden">
    <aside class="hidden md:flex w-56 xl:w-60 shrink-0 flex-col border-r border-default bg-default">
      <header class="h-14 px-4 flex items-center justify-between border-b border-default">
        <h1 class="font-semibold text-highlighted">{{ t('chat') }}</h1>
        <UIcon name="i-lucide-messages-square" class="size-4 text-muted" />
      </header>
      <div class="p-3 border-b border-default">
        <button
          class="w-full flex items-center gap-3 rounded-lg border p-3 text-left transition-colors"
          :class="
            mode === 'simulation'
              ? 'border-primary/25 bg-primary/5'
              : 'border-transparent hover:bg-elevated'
          "
          @click="mode = 'simulation'"
        >
          <span
            class="size-8 grid place-items-center rounded-md bg-primary/10 text-primary shrink-0"
            ><UIcon name="i-lucide-flask-conical" class="size-4" /></span
          ><span class="min-w-0"
            ><span class="block text-sm font-semibold">{{ t('simulation') }}</span
            ><span class="block text-xs text-muted mt-0.5">{{
              t('uxTemporarySession')
            }}</span></span
          >
        </button>
      </div>
      <div class="px-3 pt-4 pb-2">
        <p class="text-[11px] font-semibold uppercase tracking-wider text-muted px-1 mb-2">
          {{ t('realPreview') }}
        </p>
        <UInput
          v-model="sceneSearch"
          icon="i-lucide-search"
          :placeholder="t('uxSearchScenes')"
          :aria-label="t('uxSearchScenes')"
          class="w-full"
          size="sm"
        />
      </div>
      <div class="flex-1 min-h-0 overflow-y-auto px-2 py-1">
        <p v-if="!filteredScenes.length" class="px-2 py-4 text-xs leading-relaxed text-muted">
          {{ t('uxNoScenesMatch') }}
        </p>
        <button
          v-for="scene in filteredScenes"
          :key="scene.id"
          class="w-full flex items-start gap-2.5 px-3 py-3 rounded-md text-left hover:bg-elevated transition-colors mb-1"
          :class="mode === 'real' && sceneId === scene.id ? 'bg-elevated' : ''"
          @click="selectScene(scene.id)"
        >
          <UIcon
            :name="scene.kind === 'group' ? 'i-lucide-users' : 'i-lucide-user-round'"
            class="size-4 shrink-0 text-muted mt-0.5"
          /><span class="min-w-0"
            ><span class="block text-sm font-medium line-clamp-2 break-words" :title="scene.name">{{
              scene.name
            }}</span
            ><span class="block text-xs text-muted mt-1">{{ t('uxReadOnly') }}</span></span
          >
        </button>
      </div>
      <p class="p-4 text-[11px] leading-relaxed text-muted border-t border-default">
        {{ t('uxNoPersistentChats') }}
      </p>
    </aside>
    <section class="flex-1 min-w-0 min-h-0 flex flex-col bg-default">
      <header
        class="min-h-14 shrink-0 px-4 sm:px-5 py-2.5 flex items-center justify-between gap-3 border-b border-default"
      >
        <div class="min-w-0">
          <h2 class="font-semibold text-sm text-highlighted truncate" :title="selectedScene?.name">
            {{
              mode === 'simulation' ? t('simulation') : (selectedScene?.name ?? t('selectScene'))
            }}
          </h2>
          <p class="text-xs text-muted mt-0.5">
            {{ mode === 'simulation' ? t('uxTemporarySession') : t('uxReadOnly')
            }}<span v-if="mode === 'real' && coverage" class="ml-2">· {{ t(coverage) }}</span>
          </p>
        </div>
        <UButton
          v-if="mode === 'simulation'"
          color="neutral"
          variant="ghost"
          icon="i-lucide-rotate-ccw"
          size="sm"
          @click="clear"
          >{{ t('clearSession') }}</UButton
        ><UButton
          v-else
          color="neutral"
          variant="ghost"
          icon="i-lucide-refresh-cw"
          :loading="previewLoading"
          :disabled="!sceneId"
          @click="preview"
          >{{ t('refresh') }}</UButton
        >
      </header>
      <div class="md:hidden flex gap-2 p-3 border-b border-default">
        <USelect
          v-model="mode"
          :items="[
            { label: t('simulation'), value: 'simulation' },
            { label: t('realPreview'), value: 'real' },
          ]"
          :aria-label="t('chat')"
          class="w-36"
        /><USelect
          v-if="mode === 'real'"
          :model-value="sceneId"
          :items="scenes.map((s) => ({ label: s.name, value: s.id }))"
          :placeholder="t('selectScene')"
          class="flex-1 min-w-0"
          @update:model-value="selectScene"
        />
      </div>
      <div
        class="shrink-0 flex items-start gap-2 px-4 sm:px-5 py-2 text-xs leading-5 text-muted border-b border-default bg-muted/50"
      >
        <UIcon
          :name="mode === 'simulation' ? 'i-lucide-flask-conical' : 'i-lucide-eye'"
          class="size-3.5 shrink-0 mt-0.5"
        /><span>{{ mode === 'simulation' ? t('simulationBoundary') : t('previewBoundary') }}</span>
      </div>
      <div v-if="error" class="shrink-0 px-4 pt-3"><UAlert color="error" :title="error" /></div>
      <div
        ref="transcript"
        class="flex-1 min-h-0 overflow-y-auto overscroll-contain bg-muted/30 p-4 sm:p-5"
      >
        <div v-if="!messages.length && !busy" class="h-full min-h-40 grid place-items-center">
          <div class="max-w-sm text-center">
            <span
              class="inline-grid place-items-center size-12 rounded-xl bg-elevated text-muted mb-3"
              ><UIcon
                :name="mode === 'simulation' ? 'i-lucide-message-circle' : 'i-lucide-eye'"
                class="size-5"
            /></span>
            <p class="text-sm font-medium">
              {{ previewLoading ? t('uxLoading') : t('emptyChat') }}
            </p>
            <p class="text-xs leading-relaxed text-muted mt-2">
              {{ mode === 'simulation' ? t('uxChatEmptyHelp') : t('uxPreviewEmptyHelp') }}
            </p>
          </div>
        </div>
        <div v-else class="space-y-5 max-w-4xl mx-auto">
          <div
            v-for="message in messages"
            :key="message.id"
            class="flex items-start gap-2.5"
            :class="message.senderId === 'bot' ? '' : 'flex-row-reverse'"
          >
            <span
              class="size-7 shrink-0 grid place-items-center rounded-md"
              :class="
                message.senderId === 'bot' ? 'bg-primary/10 text-primary' : 'bg-elevated text-muted'
              "
              ><UIcon
                :name="message.senderId === 'bot' ? 'i-lucide-sparkles' : 'i-lucide-user-round'"
                class="size-3.5"
            /></span>
            <div class="max-w-[85%] min-w-0">
              <div
                class="flex gap-2 items-center text-[11px] text-muted mb-1.5"
                :class="message.senderId === 'bot' ? '' : 'justify-end'"
              >
                <span class="truncate">{{
                  message.senderId === 'bot'
                    ? 'HumanEngine'
                    : message.senderId === 'simulation-user'
                      ? t('you')
                      : message.senderId
                }}</span
                ><time class="tabular shrink-0">{{ time(message.sentAt) }}</time>
              </div>
              <div
                class="rounded-lg px-3.5 py-2.5 text-sm leading-6 border"
                :class="
                  message.senderId === 'bot'
                    ? 'bg-default border-default'
                    : 'bg-primary/5 border-primary/15'
                "
              >
                <div
                  v-if="message.replyToId"
                  class="border-l-2 border-primary pl-2 text-xs text-muted mb-2 break-all"
                >
                  {{ t('replyTo') }} {{ message.replyToId }}
                </div>
                <p v-if="message.status !== 'available'" class="italic text-muted">
                  {{ t('unavailableMessage') }}
                </p>
                <template v-else
                  ><template v-for="(part, index) in message.parts" :key="index"
                    ><span v-if="part.type === 'text'" class="whitespace-pre-wrap break-words">{{
                      part.text
                    }}</span
                    ><UBadge v-else-if="part.type === 'mention'" variant="soft"
                      >@{{ part.userId }}</UBadge
                    ><span v-else class="italic text-muted"
                      >[{{ part.mediaType ?? part.description ?? t('media') }}]</span
                    ></template
                  ></template
                >
              </div>
            </div>
          </div>
          <div v-if="pendingText" class="flex justify-end">
            <div
              class="max-w-[85%] rounded-lg border border-primary/15 bg-primary/5 px-3.5 py-2.5 text-sm leading-6 whitespace-pre-wrap break-words"
            >
              {{ pendingText }}
            </div>
          </div>
          <p v-if="busy" class="text-xs text-muted flex items-center gap-2">
            <UIcon name="i-lucide-loader-circle" class="size-3.5 animate-spin" />{{ t('thinking') }}
          </p>
        </div>
      </div>
      <form
        v-if="mode === 'simulation'"
        class="shrink-0 border-t border-default px-4 sm:px-5 py-3 bg-default"
        @submit.prevent="send"
      >
        <div class="max-w-4xl mx-auto">
          <UTextarea
            v-model="text"
            :placeholder="t('chatPlaceholder')"
            :aria-label="t('chatPlaceholder')"
            class="w-full"
            :rows="2"
            :disabled="busy"
            :ui="{ base: 'resize-none min-h-14' }"
            @keydown.ctrl.enter.prevent="send"
          />
          <div class="flex items-center justify-between mt-2">
            <span class="text-[11px] text-muted">{{ t('uxSendShortcut') }}</span
            ><UButton
              type="submit"
              icon="i-lucide-arrow-up"
              :loading="busy"
              :disabled="!session || !text.trim()"
              >{{ t('send') }}</UButton
            >
          </div>
        </div>
      </form>
      <div
        v-else
        class="shrink-0 border-t border-default px-5 py-4 text-xs text-muted flex items-center gap-2"
      >
        <UIcon name="i-lucide-lock-keyhole" class="size-3.5" />{{ t('previewBoundary') }}
      </div>
    </section>
  </div>
</template>
