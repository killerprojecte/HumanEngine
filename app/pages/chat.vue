<script setup lang="ts">
import type { components } from '~/types/api'
const { t } = useI18n(),
  { client } = useApi()
const coverage = ref('')
const mode = ref<'simulation' | 'real'>('simulation'),
  session = useState<string>('simulationId', () => ''),
  text = ref(''),
  busy = ref(false),
  error = ref(''),
  messages = ref<components['schemas']['Message'][]>([]),
  sceneId = ref(''),
  scenes = ref<Array<{ label: string; value: string }>>([])
let generation = 0
async function start() {
  const r = await client.POST('/api/simulations')
  if (!r.data) throw new Error(r.error?.error ?? t('networkError'))
  session.value = r.data.id
}
onMounted(async () => {
  try {
    await start()
    const r = await client.GET('/api/scenes')
    scenes.value = (r.data ?? []).map((s) => ({ label: s.name, value: s.id }))
  } catch {
    error.value = t('networkError')
  }
})
async function clear() {
  const version = ++generation
  const old = session.value
  session.value = ''
  messages.value = []
  text.value = ''
  busy.value = false
  if (old) await client.DELETE('/api/simulations/{id}', { params: { path: { id: old } } })
  if (version === generation)
    try {
      await start()
    } catch {
      error.value = t('networkError')
    }
}
async function send() {
  if (busy.value || !text.value.trim() || !session.value) return
  const version = ++generation
  const value = text.value
  text.value = ''
  busy.value = true
  error.value = ''
  try {
    const r = await client.POST('/api/simulations/{id}/messages', {
      params: { path: { id: session.value } },
      body: { text: value },
    })
    if (version !== generation) return
    if (r.data) messages.value = r.data
    else {
      error.value = r.error?.error ?? t('networkError')
      text.value = value
    }
  } catch {
    if (version === generation) {
      error.value = t('networkError')
      text.value = value
    }
  } finally {
    if (version === generation) busy.value = false
  }
}
async function preview() {
  if (!sceneId.value) return
  const version = ++generation
  error.value = ''
  try {
    const r = await client.GET('/api/scenes/{id}/history', {
      params: { path: { id: sceneId.value } },
    })
    if (version === generation) {
      messages.value = r.data?.items ?? []
      coverage.value = r.data?.coverage ?? ''
      error.value = r.error?.error ?? ''
    }
  } catch {
    error.value = t('networkError')
  }
}
watch(mode, () => {
  generation++
  messages.value = []
  busy.value = false
  if (mode.value === 'real') void preview()
  else if (session.value)
    void client
      .GET('/api/simulations/{id}', { params: { path: { id: session.value } } })
      .then((r) => {
        if (mode.value === 'simulation') messages.value = r.data ?? []
      })
})
onBeforeUnmount(() => {
  generation++
  if (session.value) {
    void client.DELETE('/api/simulations/{id}', { params: { path: { id: session.value } } })
    session.value = ''
  }
})
</script>
<template>
  <div class="space-y-5 h-full flex flex-col">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-2xl font-semibold">{{ t('chat') }}</h1>
      <div class="flex gap-2">
        <UButton :variant="mode === 'simulation' ? 'solid' : 'soft'" @click="mode = 'simulation'">{{
          t('simulation')
        }}</UButton
        ><UButton :variant="mode === 'real' ? 'solid' : 'soft'" @click="mode = 'real'">{{
          t('realPreview')
        }}</UButton>
      </div>
    </div>
    <UAlert
      icon="i-lucide-shield"
      color="neutral"
      :title="mode === 'simulation' ? t('simulationBoundary') : t('previewBoundary')"
    /><UAlert v-if="error" color="error" :title="error" />
    <div v-if="mode === 'real'" class="flex gap-3">
      <USelect
        v-model="sceneId"
        :items="scenes"
        :placeholder="t('selectScene')"
        class="flex-1"
        @update:model-value="preview"
      /><UButton variant="soft" icon="i-lucide-refresh-cw" @click="preview">{{
        t('refresh')
      }}</UButton>
    </div>
    <UBadge v-if="mode === 'real' && coverage" color="neutral">{{ t(coverage) }}</UBadge
    ><UCard class="flex-1 min-h-80"
      ><div class="space-y-5">
        <div v-if="!messages.length" class="py-16 text-center text-muted">
          <UIcon name="i-lucide-message-circle" class="text-4xl mb-3" />
          <p>{{ t('emptyChat') }}</p>
        </div>
        <div
          v-for="message in messages"
          :key="message.id"
          class="flex"
          :class="message.senderId === 'bot' ? 'justify-start' : 'justify-end'"
        >
          <div
            class="max-w-[85%] rounded-2xl p-4"
            :class="message.senderId === 'bot' ? 'bg-elevated' : 'bg-primary/10'"
          >
            <div class="text-xs text-muted mb-2">
              {{
                message.senderId === 'bot'
                  ? 'HumanEngine'
                  : message.senderId === 'simulation-user'
                    ? t('you')
                    : message.senderId
              }}
              · {{ new Date(message.sentAt).toLocaleTimeString() }}
            </div>
            <div
              v-if="message.replyToId"
              class="border-l-2 border-primary pl-2 text-xs text-muted mb-2"
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
        <p v-if="busy" class="text-sm text-muted animate-pulse">{{ t('thinking') }}</p>
      </div></UCard
    >
    <form v-if="mode === 'simulation'" class="space-y-3" @submit.prevent="send">
      <UTextarea
        v-model="text"
        :placeholder="t('chatPlaceholder')"
        class="w-full"
        :rows="3"
        :disabled="busy"
        @keydown.ctrl.enter.prevent="send"
      />
      <div class="flex justify-between">
        <UButton color="neutral" variant="ghost" icon="i-lucide-trash-2" @click="clear">{{
          t('clearSession')
        }}</UButton
        ><UButton
          type="submit"
          icon="i-lucide-send"
          :loading="busy"
          :disabled="!session || !text.trim()"
          >{{ t('send') }}</UButton
        >
      </div>
    </form>
  </div>
</template>
