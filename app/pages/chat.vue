<script setup lang="ts">
import type { components } from '~/types/api'
import { humanName } from '~/utils/chatIdentity'
type Message = components['schemas']['Message'] & {
  senderDisplayName?: string
  senderAvatarUrl?: string
  senderPlatformId?: string
}
type Scene = components['schemas']['Scene'] & { avatarUrl?: string }
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
const scenes = ref<Scene[]>([]),
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
const mobileList = ref(true),
  scenesLoading = ref(true),
  scenesError = ref('')
const identityMessage = ref<Message>(),
  identityOpen = ref(false)
const simulationUnread = ref(0)
const lastMessages = ref<Record<string, Message | undefined>>({})
const sceneName = (scene: Scene) =>
  humanName(scene.name) ||
  t(scene.kind === 'group' ? 'cwUnnamedGroup' : 'cwUnnamedPrivate', {
    number: scenes.value.findIndex((item) => item.id === scene.id) + 1,
  })
const senderName = (message: Message) =>
  message.senderId === 'simulation-user'
    ? t('you')
    : humanName(message.senderDisplayName) ||
      t(message.senderId === 'bot' ? 'cfBot' : 'cwUnknownMember')
const messagePreview = (message?: Message) =>
  !message
    ? t('cwHistoryPreview')
    : message.status !== 'available'
      ? t('unavailableMessage')
      : message.parts
          .map((part) =>
            part.type === 'text'
              ? part.text
              : part.type === 'mention'
                ? t('cwMention')
                : t('media'),
          )
          .join(' ')
const filteredScenes = computed(() =>
  scenes.value.filter((scene) =>
    sceneName(scene).toLowerCase().includes(search.value.trim().toLowerCase()),
  ),
)
const groupedScenes = computed(() =>
  ['group', 'private'].map((kind) => ({
    kind,
    items: filteredScenes.value.filter((scene) => scene.kind === kind),
  })),
)
function selectSimulation() {
  mode.value = 'simulation'
  simulationUnread.value = 0
  mobileList.value = false
}
function showIdentity(message: Message) {
  identityMessage.value = message
  identityOpen.value = true
}
function mentionName(userId: string | undefined) {
  return (
    humanName(shown.value.find((message) => message.senderId === userId)?.senderDisplayName) ||
    t('cwUnknownMember')
  )
}
function replyName(id: string) {
  const original = shown.value.find((message) => message.id === id)
  return original
    ? senderName(original) + '：' + messagePreview(original).slice(0, 80)
    : t('cwOriginalUnavailable')
}
async function loadScenes() {
  if (disposed) return
  scenesLoading.value = true
  scenesError.value = ''
  try {
    const result = await client.GET('/api/scenes', { signal: controller.signal })
    if (disposed) return
    if (!result.data || result.error) throw new Error('Unavailable')
    scenes.value = result.data
  } catch {
    if (!disposed) scenesError.value = t('networkError')
  } finally {
    if (!disposed) scenesLoading.value = false
  }
}
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
      if (mode.value !== 'simulation') simulationUnread.value++
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
    lastMessages.value[sceneId.value] = r.data.items.at(-1)
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
  mobileList.value = false
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
  void loadScenes()
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
    <div class="flex flex-1 min-h-0 overflow-hidden">
      <aside
        class="conversation-sidebar shrink-0 flex-col border-r border-default bg-default"
        :class="mobileList ? 'flex w-full md:w-72' : 'hidden md:flex md:w-72'"
        :aria-label="t('cwConversations')"
      >
        <div class="px-5 pt-5 pb-4 space-y-4">
          <div class="flex items-center justify-between">
            <h1 class="text-lg font-semibold">{{ t('cwConversations') }}</h1>
            <span class="text-xs text-muted">{{ scenes.length + 1 }}</span>
          </div>
          <UInput
            v-model="search"
            icon="i-lucide-search"
            class="w-full"
            :placeholder="t('cwSearch')"
            :aria-label="t('cwSearch')"
          />
        </div>
        <div class="px-3 pb-3 border-b border-default">
          <button
            class="conversation-row"
            :class="{ 'conversation-selected': mode === 'simulation' }"
            :aria-pressed="mode === 'simulation'"
            @click="selectSimulation"
          >
            <span class="simulation-avatar"><UIcon name="i-lucide-sparkles" class="size-5" /></span>
            <span class="min-w-0 flex-1"
              ><span class="flex items-center gap-2"
                ><span class="font-medium truncate">{{ t('cwSimulation') }}</span
                ><span
                  v-if="simulationUnread"
                  class="unread-badge"
                  :aria-label="t('cwUnread', { count: simulationUnread })"
                  >{{ simulationUnread }}</span
                ></span
              ><span class="block mt-1 text-xs text-muted truncate">{{
                submitted ? t('thinking') : t('cwSimulationPreview')
              }}</span></span
            >
          </button>
        </div>
        <div class="flex-1 min-h-0 overflow-y-auto px-3 py-3">
          <p v-if="scenesLoading" role="status" class="px-3 py-6 text-sm text-muted">
            {{ t('uxLoading') }}
          </p>
          <div v-if="scenesError" role="alert" class="p-3 text-sm text-error">
            {{ scenesError
            }}<UButton class="mt-2" size="sm" variant="outline" @click="loadScenes">{{
              t('refresh')
            }}</UButton>
          </div>
          <template v-for="group in groupedScenes" :key="group.kind">
            <h2 class="px-3 pt-3 pb-2 text-xs font-medium text-muted">
              {{ t(group.kind === 'group' ? 'cfGroup' : 'cfPrivate') }}
              <span class="ml-1 opacity-60">{{ group.items.length }}</span>
            </h2>
            <button
              v-for="scene in group.items"
              :key="scene.id"
              class="conversation-row"
              :class="{ 'conversation-selected': mode === 'real' && scene.id === sceneId }"
              :aria-pressed="mode === 'real' && scene.id === sceneId"
              @click="selectScene(scene.id)"
            >
              <ChatAvatar
                :name="sceneName(scene)"
                :identity="scene.id"
                :src="scene.avatarUrl"
                :group="scene.kind === 'group'"
              />
              <span class="min-w-0 flex-1"
                ><span class="block font-medium truncate">{{ sceneName(scene) }}</span
                ><span class="block mt-1 text-xs text-muted truncate">{{
                  messagePreview(lastMessages[scene.id])
                }}</span></span
              >
            </button>
          </template>
          <p
            v-if="!scenesLoading && !scenesError && !filteredScenes.length"
            class="px-3 py-6 text-sm text-muted"
          >
            {{ t(search ? 'uxNoScenesMatch' : 'emptyScenes') }}
          </p>
        </div>
        <p class="p-4 border-t border-default text-[11px] leading-5 text-muted flex gap-2">
          <UIcon name="i-lucide-shield-check" class="size-4 shrink-0 mt-0.5" />{{
            t('cwSidebarSafety')
          }}
        </p>
      </aside>
      <section
        class="flex-1 min-h-0 min-w-0 flex-col"
        :class="mobileList ? 'hidden md:flex' : 'flex'"
      >
        <header
          class="shrink-0 px-4 sm:px-6 py-4 border-b border-default flex flex-wrap items-start justify-between gap-3"
        >
          <UButton
            class="md:hidden shrink-0"
            color="neutral"
            variant="ghost"
            icon="i-lucide-arrow-left"
            :aria-label="t('cwBack')"
            @click="mobileList = true"
          />
          <span v-if="mode === 'simulation'" class="simulation-avatar"
            ><UIcon name="i-lucide-sparkles" class="size-5"
          /></span>
          <ChatAvatar
            v-else-if="selectedScene"
            :name="sceneName(selectedScene)"
            :identity="selectedScene.id"
            :src="selectedScene.avatarUrl"
            :group="selectedScene.kind === 'group'"
          />
          <div class="min-w-0 flex-1">
            <h2 class="font-medium break-words">
              {{
                mode === 'simulation'
                  ? t('cwSimulation')
                  : selectedScene
                    ? sceneName(selectedScene)
                    : t('selectScene')
              }}
            </h2>
            <p class="text-sm text-muted mt-1">
              {{ t(mode === 'simulation' ? 'cwSimulationSafety' : 'cwReadOnly') }}
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
              class="flex items-start gap-3"
              :class="{
                'flex-row-reverse': mode === 'simulation' && message.senderId === 'simulation-user',
              }"
            >
              <button
                class="shrink-0 rounded-xl focus-visible:outline-2 focus-visible:outline-primary"
                :aria-label="t('cwIdentity', { name: senderName(message) })"
                @click="showIdentity(message)"
              >
                <ChatAvatar
                  :name="senderName(message)"
                  :identity="message.senderId"
                  :src="message.senderAvatarUrl"
                />
              </button>
              <div
                class="min-w-0 max-w-[85%]"
                :class="{
                  'text-right': mode === 'simulation' && message.senderId === 'simulation-user',
                }"
              >
                <div
                  class="flex flex-wrap items-baseline gap-x-3 gap-y-1 mb-1.5 text-xs text-muted"
                  :class="{
                    'justify-end': mode === 'simulation' && message.senderId === 'simulation-user',
                  }"
                >
                  <button
                    class="font-medium text-default hover:text-primary break-words"
                    @click="showIdentity(message)"
                  >
                    {{ senderName(message) }}</button
                  ><time :datetime="message.sentAt">{{ time(message.sentAt) }}</time>
                </div>
                <div
                  class="message-bubble text-left rounded-2xl px-4 py-3 text-sm leading-6 border border-default"
                  :class="
                    mode === 'simulation' && message.senderId === 'simulation-user'
                      ? 'bg-primary/10 rounded-tr-sm'
                      : 'bg-default rounded-tl-sm'
                  "
                >
                  <p
                    v-if="message.replyToId"
                    class="mb-2 border-l-2 border-primary pl-2 text-xs text-muted break-words"
                  >
                    {{ t('replyTo') }} {{ replyName(message.replyToId) }}
                  </p>
                  <p v-if="message.status !== 'available'" class="italic text-muted">
                    {{ t('unavailableMessage') }}
                  </p>
                  <template v-else
                    ><template v-for="(part, index) in message.parts" :key="index"
                      ><span v-if="part.type === 'text'" class="whitespace-pre-wrap break-words">{{
                        part.text
                      }}</span
                      ><span v-else-if="part.type === 'mention'" class="text-primary break-words">
                        @{{ mentionName(part.userId) }} </span
                      ><span v-else class="text-muted break-words"
                        >[{{ part.mediaType ?? part.description ?? t('media') }}]</span
                      ></template
                    ></template
                  >
                </div>
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
        <div
          v-if="mode === 'real'"
          class="shrink-0 border-t border-default px-6 py-5 text-sm text-muted flex items-center justify-center gap-2"
        >
          <UIcon name="i-lucide-lock-keyhole" class="size-4" />{{ t('cwReadOnlyComposer') }}
        </div>
      </section>
    </div>
    <UModal
      v-model:open="identityOpen"
      :title="identityMessage ? senderName(identityMessage) : t('cwUnknownMember')"
      :description="t('cwIdentityHelp')"
    >
      <template #body
        ><dl v-if="identityMessage" class="space-y-4 text-sm">
          <div v-if="identityMessage.senderPlatformId">
            <dt class="text-muted">{{ t('cwPlatformAccount') }}</dt>
            <dd class="mt-1 break-all">{{ identityMessage.senderPlatformId }}</dd>
          </div>
          <div>
            <dt class="text-muted">{{ t('cwInternalIdentity') }}</dt>
            <dd class="font-mono text-xs mt-1 break-all">{{ identityMessage.senderId }}</dd>
          </div>
        </dl></template
      >
    </UModal>
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

<style scoped>
.conversation-row {
  display: flex;
  width: 100%;
  align-items: center;
  gap: 12px;
  padding: 12px;
  text-align: left;
  border-radius: 12px;
  font-size: 14px;
  transition: background 120ms;
}
.conversation-row:hover {
  background: var(--ui-bg-elevated);
}
.conversation-row:focus-visible {
  outline: 2px solid var(--ui-primary);
  outline-offset: -2px;
}
.conversation-selected,
.conversation-selected:hover {
  background: color-mix(in srgb, var(--ui-primary) 10%, var(--ui-bg));
}
.simulation-avatar {
  display: flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 auto;
  width: 38px;
  height: 38px;
  color: var(--ui-primary);
  background: color-mix(in srgb, var(--ui-primary) 13%, var(--ui-bg));
  border-radius: 13px;
}
.unread-badge {
  background: var(--ui-primary);
  color: var(--ui-bg);
  border-radius: 999px;
  min-width: 18px;
  padding: 1px 5px;
  font-size: 10px;
  text-align: center;
}
.message-bubble {
  overflow-wrap: anywhere;
}
</style>
