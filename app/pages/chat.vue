<script setup lang="ts">
import type { components } from '~/types/api'
type Message = components['schemas']['Message']
const { t, locale } = useI18n(),
  { client } = useApi()
const mode = ref<'simulation' | 'real'>('simulation')
const session = ref(''),
  draft = ref(''),
  submitted = ref('')
const operation = ref<'idle' | 'creating' | 'sending' | 'reading' | 'ending'>('idle')
const uncertain = ref(false),
  simulationError = ref(''),
  simulationMessages = ref<Message[]>([])
const scenes = ref<components['schemas']['Scene'][]>([]),
  sceneId = ref(''),
  search = ref('')
const history = ref<Message[]>([]),
  historyLoading = ref(false),
  historyError = ref(''),
  coverage = ref(''),
  historyReason = ref(''),
  historyAt = ref('')
const transcript = ref<HTMLElement>(),
  following = ref(true),
  resetOpen = ref(false),
  discardOpen = ref(false)
let leaveResolve: ((value: boolean) => void) | undefined
let disposed = false,
  historyGeneration = 0,
  operationGeneration = 0,
  sendController: AbortController | undefined,
  historyController: AbortController | undefined
const controller = new AbortController()
const busy = computed(() => operation.value !== 'idle')
const resetBlocked = computed(() => ['creating', 'reading', 'ending'].includes(operation.value))
const selectedScene = computed(() => scenes.value.find((s) => s.id === sceneId.value))
const filteredScenes = computed(() =>
  scenes.value.filter((s) => s.name.toLowerCase().includes(search.value.trim().toLowerCase())),
)
const shown = computed(() =>
  mode.value === 'simulation' ? simulationMessages.value : history.value,
)
const needsDiscard = computed(
  () =>
    !!draft.value.trim() ||
    !!submitted.value ||
    simulationMessages.value.length > 0 ||
    operation.value === 'sending',
)
const time = (value: string) =>
  new Date(value).toLocaleTimeString(locale.value, { hour: '2-digit', minute: '2-digit' })
async function bottom() {
  await nextTick()
  if (transcript.value) transcript.value.scrollTop = transcript.value.scrollHeight
}
function follow() {
  following.value = true
  void bottom()
}
function scroll() {
  const p = transcript.value
  if (p && p.scrollHeight - p.scrollTop - p.clientHeight > 100) following.value = false
}
async function createSession() {
  const r = await client.POST('/api/simulations')
  if (!r.data) throw new Error(r.error?.error ?? t('networkError'))
  if (disposed) {
    void client
      .DELETE('/api/simulations/{id}', { params: { path: { id: r.data.id } } })
      .catch(() => {})
    return
  }
  session.value = r.data.id
}
async function initialize() {
  if (busy.value || session.value) return
  operation.value = 'creating'
  simulationError.value = ''
  try {
    await createSession()
  } catch {
    if (!disposed) simulationError.value = t('cfCreateFailed')
  } finally {
    if (!disposed) operation.value = 'idle'
  }
}
async function reset() {
  if (!resetOpen.value || resetBlocked.value) return
  const version = ++operationGeneration
  sendController?.abort()
  sendController = undefined
  operation.value = 'ending'
  simulationError.value = ''
  try {
    if (session.value) {
      const result = await client.DELETE('/api/simulations/{id}', {
        params: { path: { id: session.value } },
        signal: controller.signal,
      })
      if (!result.data || result.error) throw new Error('Session end unconfirmed')
    }
    if (disposed || version !== operationGeneration) return
    session.value = ''
    simulationMessages.value = []
    submitted.value = ''
    draft.value = ''
    uncertain.value = false
    resetOpen.value = false
    operation.value = 'creating'
    await createSession()
    following.value = true
  } catch {
    if (!disposed && version === operationGeneration) {
      uncertain.value = !!session.value
      simulationError.value = t(session.value ? 'cfEndFailed' : 'cfCreateFailed')
    }
  } finally {
    if (!disposed && version === operationGeneration) operation.value = 'idle'
  }
}
async function reconcile() {
  if (busy.value || !session.value) return
  operation.value = 'reading'
  simulationError.value = ''
  try {
    const r = await client.GET('/api/simulations/{id}', {
      params: { path: { id: session.value } },
      signal: controller.signal,
    })
    if (disposed) return
    if (!r.data || r.error) throw new Error('Read failed')
    simulationMessages.value = r.data
  } catch {
    if (!disposed) simulationError.value = t('networkError')
  } finally {
    if (!disposed) operation.value = 'idle'
  }
}
async function send() {
  if (
    busy.value ||
    uncertain.value ||
    !session.value ||
    mode.value !== 'simulation' ||
    !draft.value.trim()
  )
    return
  const version = ++operationGeneration
  const requestController = new AbortController()
  sendController = requestController
  const value = draft.value
  draft.value = ''
  submitted.value = value
  operation.value = 'sending'
  simulationError.value = ''
  following.value = true
  void bottom()
  try {
    const r = await client.POST('/api/simulations/{id}/messages', {
      params: { path: { id: session.value } },
      body: { text: value },
      signal: AbortSignal.any([controller.signal, requestController.signal]),
    })
    if (disposed || version !== operationGeneration) return
    if (r.data && !r.error) {
      simulationMessages.value = r.data
      submitted.value = ''
    } else {
      uncertain.value = true
      simulationError.value = t('cfUnknownSend')
    }
  } catch {
    if (!disposed && version === operationGeneration) {
      uncertain.value = true
      simulationError.value = t('cfUnknownSend')
    }
  } finally {
    if (!disposed && version === operationGeneration) {
      operation.value = 'idle'
      sendController = undefined
    }
  }
}
async function preview() {
  if (!sceneId.value || mode.value !== 'real') return
  historyController?.abort()
  historyController = new AbortController()
  const version = ++historyGeneration
  historyLoading.value = true
  historyError.value = ''
  try {
    const r = await client.GET('/api/scenes/{id}/history', {
      params: { path: { id: sceneId.value } },
      signal: historyController.signal,
    })
    if (disposed || version !== historyGeneration) return
    if (!r.data || r.error) {
      historyError.value = r.error?.error ?? t('networkError')
      return
    }
    history.value = r.data.items
    coverage.value = r.data.coverage
    historyReason.value = r.data.reason ?? ''
    historyAt.value = new Date().toISOString()
  } catch {
    if (!disposed && version === historyGeneration) historyError.value = t('networkError')
  } finally {
    if (!disposed && version === historyGeneration) historyLoading.value = false
  }
}
function selectScene(id: string) {
  sceneId.value = id
  mode.value = 'real'
}
watch([mode, sceneId], () => {
  historyController?.abort()
  ++historyGeneration
  historyLoading.value = false
  following.value = true
  if (mode.value === 'real') {
    history.value = []
    coverage.value = ''
    historyReason.value = ''
    historyAt.value = ''
    historyError.value = ''
    void preview()
  }
  void bottom()
})
watch([shown, submitted], () => {
  if (following.value) void bottom()
})
function settleLeave(discard: boolean) {
  const resolve = leaveResolve
  leaveResolve = undefined
  discardOpen.value = false
  resolve?.(discard)
}
onBeforeRouteLeave(() => {
  if (!needsDiscard.value) return true
  leaveResolve?.(false)
  discardOpen.value = true
  return new Promise<boolean>((resolve) => {
    leaveResolve = resolve
  })
})
function beforeUnload(event: BeforeUnloadEvent) {
  if (needsDiscard.value) {
    event.preventDefault()
    event.returnValue = ''
  }
}
onMounted(() => {
  window.addEventListener('beforeunload', beforeUnload)
  void initialize()
  void client
    .GET('/api/scenes', { signal: controller.signal })
    .then((r) => {
      if (!disposed) {
        scenes.value = r.data ?? []
        if (r.error) historyError.value = r.error.error
      }
    })
    .catch(() => {
      if (!disposed) historyError.value = t('networkError')
    })
})
onBeforeUnmount(() => {
  disposed = true
  ++operationGeneration
  sendController?.abort()
  ++historyGeneration
  historyController?.abort()
  controller.abort()
  leaveResolve?.(false)
  window.removeEventListener('beforeunload', beforeUnload)
  if (session.value)
    void client
      .DELETE('/api/simulations/{id}', { params: { path: { id: session.value } } })
      .catch(() => {})
})
</script>
<template>
  <div class="flex flex-1 min-h-0 flex-col overflow-hidden">
    <header class="shrink-0 border-b border-default p-4 sm:px-6 sm:py-5">
      <h1 class="page-heading">{{ t('chat') }}</h1>
      <p class="mt-1 text-sm text-muted">{{ t('cfHelp') }}</p>
      <div class="mt-4 flex flex-wrap gap-2" role="group" :aria-label="t('cfMode')">
        <UButton
          :variant="mode === 'simulation' ? 'solid' : 'outline'"
          :aria-pressed="mode === 'simulation'"
          @click="mode = 'simulation'"
          >{{ t('simulation') }}</UButton
        >
        <UButton
          :variant="mode === 'real' ? 'solid' : 'outline'"
          :aria-pressed="mode === 'real'"
          @click="mode = 'real'"
          >{{ t('cfHistory') }}</UButton
        >
      </div>
    </header>
    <div class="flex flex-1 min-h-0 overflow-hidden">
      <aside
        v-if="mode === 'real'"
        class="hidden md:flex w-60 shrink-0 flex-col border-r border-default p-4 gap-3"
      >
        <label for="chat-scene-search" class="text-sm font-medium">{{ t('selectScene') }}</label>
        <UInput id="chat-scene-search" v-model="search" :placeholder="t('uxSearchScenes')" />
        <div class="overflow-y-auto min-h-0 space-y-1">
          <button
            v-for="scene in filteredScenes"
            :key="scene.id"
            class="w-full rounded-md p-3 text-left focus-visible:outline-2 focus-visible:outline-primary"
            :class="scene.id === sceneId ? 'bg-primary/10' : 'hover:bg-elevated'"
            :aria-pressed="scene.id === sceneId"
            @click="selectScene(scene.id)"
          >
            <span class="block text-sm break-words">{{ scene.name }}</span
            ><span class="mt-1 block text-xs text-muted">{{
              t(scene.kind === 'group' ? 'cfGroup' : 'cfPrivate')
            }}</span>
          </button>
          <p v-if="!filteredScenes.length" class="text-sm text-muted py-4">
            {{ t('uxNoScenesMatch') }}
          </p>
        </div>
      </aside>
      <section class="flex flex-1 min-h-0 min-w-0 flex-col">
        <header
          class="shrink-0 px-4 sm:px-6 py-4 border-b border-default flex flex-wrap items-start justify-between gap-3"
        >
          <div class="min-w-0 flex-1">
            <h2 class="font-medium break-words">
              {{
                mode === 'simulation' ? t('simulation') : (selectedScene?.name ?? t('selectScene'))
              }}
            </h2>
            <p class="text-sm text-muted mt-1">
              {{ t(mode === 'simulation' ? 'cfSimulationHelp' : 'previewBoundary') }}
            </p>
          </div>
          <UButton
            v-if="mode === 'simulation'"
            class="shrink-0 whitespace-nowrap"
            color="neutral"
            variant="outline"
            :disabled="resetBlocked"
            @click="resetOpen = true"
            >{{ t('cfNewSession') }}</UButton
          >
          <UButton
            v-else
            class="shrink-0 whitespace-nowrap"
            color="neutral"
            variant="outline"
            :loading="historyLoading"
            :disabled="!sceneId || historyLoading"
            @click="preview"
            >{{ t('refresh') }}</UButton
          >
          <UFormField v-if="mode === 'real'" :label="t('selectScene')" class="w-full md:hidden"
            ><USelect
              v-model="sceneId"
              :items="scenes.map((s) => ({ label: s.name, value: s.id }))"
              class="w-full"
              :placeholder="t('selectScene')"
          /></UFormField>
        </header>
        <div v-if="mode === 'simulation'" class="shrink-0 px-4 sm:px-6 pt-3 space-y-2">
          <UAlert v-if="simulationError" color="error" :title="simulationError" />
          <UAlert
            v-if="uncertain"
            color="warning"
            variant="subtle"
            :ui="{
              root: 'bg-amber-50 dark:bg-amber-950/50 ring-amber-200 dark:ring-amber-800',
              title: 'text-amber-950 dark:text-amber-100',
              description: 'text-amber-900 dark:text-amber-200',
              icon: 'text-amber-700 dark:text-amber-300',
            }"
            :title="t('cfUnknownTitle')"
            :description="t('cfRecoveryHelp')"
          />
          <div v-if="uncertain" class="flex gap-2">
            <UButton variant="outline" :disabled="busy" @click="reconcile">{{
              t('cfReadSession')
            }}</UButton>
          </div>
          <UButton v-if="!session && !busy" variant="outline" @click="initialize">{{
            t('cfStart')
          }}</UButton>
          <p
            v-if="operation === 'creating' || operation === 'ending' || operation === 'reading'"
            role="status"
            class="text-sm text-muted"
          >
            {{ t('cfWorking') }}
          </p>
        </div>
        <div v-else class="shrink-0 px-4 sm:px-6 pt-3 space-y-2">
          <UAlert v-if="historyError" color="error" :title="historyError" />
          <p v-if="historyAt" class="text-xs text-muted">
            {{ t(historyError ? 'cfStaleAt' : 'cfFetchedAt', { time: time(historyAt) }) }} ·
            {{ t(coverage) }}
          </p>
          <p v-if="historyReason" class="text-sm text-muted break-words">{{ historyReason }}</p>
        </div>
        <div
          ref="transcript"
          class="flex-1 min-h-0 overflow-y-auto p-4 sm:p-6 bg-muted/20"
          tabindex="0"
          :aria-label="t('cfTranscript')"
          @scroll.passive="scroll"
        >
          <p
            v-if="!shown.length && (mode === 'real' || !submitted)"
            class="py-10 text-center text-sm text-muted"
          >
            {{
              mode === 'simulation'
                ? t('cfEmptySimulation')
                : !sceneId
                  ? t('selectScene')
                  : historyLoading
                    ? t('uxLoading')
                    : coverage === 'unavailable' || historyError
                      ? t('cfHistoryUnavailable')
                      : t('cfEmptyHistory')
            }}
          </p>
          <div class="max-w-4xl mx-auto space-y-5">
            <article
              v-for="message in shown"
              :key="message.id"
              class="min-w-0"
              :class="
                mode === 'simulation' && message.senderId === 'simulation-user'
                  ? 'ml-auto w-fit max-w-[90%]'
                  : 'max-w-full'
              "
            >
              <div class="flex flex-wrap gap-x-3 gap-y-1 mb-1.5 text-xs text-muted">
                <span class="font-medium break-all">{{
                  message.senderId === 'bot'
                    ? t('cfBot')
                    : message.senderId === 'simulation-user'
                      ? t('you')
                      : message.senderId
                }}</span
                ><time :datetime="message.sentAt">{{ time(message.sentAt) }}</time>
              </div>
              <div
                class="rounded-lg border border-default px-4 py-3 text-sm leading-6"
                :class="
                  message.senderId === 'bot'
                    ? 'bg-default'
                    : mode === 'real'
                      ? 'bg-default/60'
                      : 'bg-primary/5'
                "
              >
                <p
                  v-if="message.replyToId"
                  class="mb-2 border-l-2 border-primary pl-2 text-xs text-muted break-all"
                >
                  {{ t('replyTo') }} {{ message.replyToId }}
                </p>
                <p v-if="message.status !== 'available'" class="italic text-muted">
                  {{ t('unavailableMessage') }}
                </p>
                <template v-else
                  ><template v-for="(part, index) in message.parts" :key="index"
                    ><span v-if="part.type === 'text'" class="whitespace-pre-wrap break-words">{{
                      part.text
                    }}</span
                    ><span v-else-if="part.type === 'mention'" class="text-primary break-all">
                      @{{ part.userId }} </span
                    ><span v-else class="text-muted break-words">
                      [{{ part.mediaType ?? part.description ?? t('media') }}]
                    </span></template
                  ></template
                >
              </div>
            </article>
            <div
              v-if="mode === 'simulation' && submitted"
              class="border border-warning/40 rounded-lg p-4"
            >
              <p class="text-xs text-muted mb-2">
                {{ t(uncertain ? 'cfSubmittedUnknown' : 'cfSubmitted') }}
              </p>
              <p class="whitespace-pre-wrap break-words text-sm">{{ submitted }}</p>
            </div>
            <p
              v-if="mode === 'simulation' && operation === 'sending'"
              role="status"
              class="text-sm text-muted"
            >
              {{ t('thinking') }}
            </p>
          </div>
        </div>
        <div v-if="!following" class="shrink-0 flex justify-center border-t border-default py-2">
          <UButton size="sm" variant="ghost" @click="follow">{{ t('cfFollow') }}</UButton>
        </div>
        <form
          v-if="mode === 'simulation'"
          class="shrink-0 border-t border-default p-4 sm:px-6"
          @submit.prevent="send"
        >
          <div class="max-w-4xl mx-auto">
            <UTextarea
              v-model="draft"
              class="w-full"
              :rows="2"
              :maxlength="12000"
              :disabled="busy || uncertain || !session"
              :aria-label="t('chatPlaceholder')"
              :placeholder="t('chatPlaceholder')"
              @keydown.ctrl.enter.prevent="send"
            />
            <div class="flex justify-between items-center gap-3 mt-2">
              <span class="text-xs text-muted">{{ t('uxSendShortcut') }}</span
              ><UButton
                type="submit"
                :loading="operation === 'sending'"
                :disabled="busy || uncertain || !session || !draft.trim()"
                >{{ t('send') }}</UButton
              >
            </div>
          </div>
        </form>
      </section>
    </div>
    <UModal
      :open="resetOpen"
      :dismissible="!resetBlocked"
      :title="t('cfNewSession')"
      :description="t('cfResetHelp')"
      @update:open="
        (value) => {
          if (!resetBlocked) resetOpen = value
        }
      "
      ><template #body
        ><UAlert v-if="simulationError" color="error" :title="simulationError" /></template
      ><template #footer
        ><div class="flex justify-end gap-2 w-full">
          <UButton
            color="neutral"
            variant="outline"
            :disabled="resetBlocked"
            @click="resetOpen = false"
            >{{ t('cancel') }}</UButton
          ><UButton :loading="resetBlocked" :disabled="resetBlocked || !resetOpen" @click="reset">{{
            t('cfNewSession')
          }}</UButton>
        </div></template
      ></UModal
    >
    <UModal
      :open="discardOpen"
      :title="t('cfLeaveTitle')"
      :description="t('cfLeaveHelp')"
      @update:open="
        (value) => {
          if (!value) settleLeave(false)
        }
      "
      ><template #footer
        ><div class="flex justify-end gap-2 w-full">
          <UButton color="neutral" variant="outline" @click="settleLeave(false)">{{
            t('cancel')
          }}</UButton
          ><UButton color="warning" @click="settleLeave(true)">{{ t('cfLeave') }}</UButton>
        </div></template
      ></UModal
    >
  </div>
</template>
