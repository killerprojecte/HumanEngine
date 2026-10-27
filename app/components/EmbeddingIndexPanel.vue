<script setup lang="ts">
import type { components } from '~/types/api'
const props = defineProps<{ configBusy: boolean }>()
const { t } = useI18n()
const { client } = useApi()
type Status = components['schemas']['EmbeddingIndexStatus']
type Action = 'start' | 'pause' | 'cancel' | 'resume'
const status = ref<Status>(),
  busy = ref(false),
  error = ref(''),
  uncertain = ref(false)
const pending = ref<Action>(),
  confirmUnknown = ref(false)
const actionLocked = computed(() => busy.value || props.configBusy || uncertain.value)
let disposed = false
let refreshQueued = false
let timer: ReturnType<typeof setInterval> | undefined
async function refresh() {
  if (disposed) return
  if (busy.value) {
    refreshQueued = true
    return
  }
  refreshQueued = false
  busy.value = true
  try {
    const result = await client.GET('/api/embedding-index')
    if (!result.data) throw new Error(t('networkError'))
    if (!disposed) {
      status.value = result.data
      uncertain.value = false
      error.value = ''
      pending.value = undefined
    }
  } catch {
    if (!disposed) error.value = t('networkError')
  } finally {
    busy.value = false
    if (refreshQueued && !disposed) void refresh()
  }
}
function request(action: Action) {
  if (actionLocked.value) return
  pending.value = action
  confirmUnknown.value = false
}
async function execute() {
  const action = pending.value
  if (
    !action ||
    actionLocked.value ||
    (action === 'resume' && status.value?.job?.requiresRetryConfirmation && !confirmUnknown.value)
  )
    return
  busy.value = true
  pending.value = undefined
  error.value = ''
  try {
    const result = await client.POST('/api/embedding-index/{action}', {
      params: { path: { action } },
      body: {
        confirmUnknownRequest: action === 'resume' && confirmUnknown.value,
        expectedJobId: status.value?.job?.id ?? null,
        ...(action === 'start'
          ? { expectedEncoderRevision: status.value?.selected?.encoderRevision }
          : {}),
      },
    })
    if (!result.data) {
      uncertain.value = true
      error.value = t('eiActionUnavailable')
      return
    }
    if (!disposed) status.value = result.data
  } catch {
    // Never repeat a potentially billed job automatically after losing its response.
    if (!disposed) {
      uncertain.value = true
      error.value = t('eiResponseUnknown')
    }
  } finally {
    busy.value = false
    if (refreshQueued && !disposed) void refresh()
  }
}
defineExpose({ refresh })
onMounted(() => {
  void refresh()
  timer = setInterval(() => {
    if (!busy.value && status.value?.job?.state === 'running' && !pending.value && !uncertain.value)
      void refresh()
  }, 3000)
})
onBeforeUnmount(() => {
  disposed = true
  if (timer) clearInterval(timer)
})
</script>
<template>
  <section class="rounded-lg border border-default p-4 space-y-3" :aria-label="t('eiTitle')">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h2 class="font-semibold">{{ t('eiTitle') }}</h2>
      <UButton variant="ghost" color="neutral" :disabled="busy" :loading="busy" @click="refresh">{{
        t('refresh')
      }}</UButton>
    </div>
    <p class="text-sm text-muted">{{ t('eiHelp') }}</p>
    <UAlert v-if="error" color="warning" :title="error" />
    <template v-if="status">
      <p class="text-sm font-medium">{{ t(`eiState_${status.state}`) }}</p>
      <p v-if="status.selected" class="text-sm break-all">
        {{
          t('eiSelected', {
            model: status.selected.modelId,
            dimensions: status.selected.requestedDimensions ?? t('eiNative'),
          })
        }}
      </p>
      <p class="text-sm">{{ t('eiCoverage', status.coverage) }}</p>
      <p v-if="status.active" class="text-xs break-all">
        {{
          t('eiActive', {
            model: status.active.modelId,
            dimensions: status.active.dimensions,
            generation: status.active.generationId,
          })
        }}
      </p>
      <p class="text-xs text-muted">{{ t('eiRetained', status.retained) }}</p>
      <div v-if="status.job" class="rounded border border-default p-3 space-y-2">
        <p class="text-sm">
          {{
            t('eiJob', {
              state: t(`eiJob_${status.job.state}`),
              processed: status.job.processed,
              total: status.job.total,
            })
          }}
        </p>
        <p class="text-xs break-all">
          {{ t('eiStaging') }}: {{ status.job.id }} ·
          {{ status.job.dimensions ?? t('eiAwaitingDimensions') }}
        </p>
        <p v-if="status.job.error" class="text-xs break-all">{{ status.job.error }}</p>
        <p
          v-if="
            status.job.requiresRetryConfirmation && ['paused', 'failed'].includes(status.job.state)
          "
          class="text-sm text-warning"
        >
          {{ t('eiUnknownWarning') }}
        </p>
      </div>
      <div class="flex flex-wrap gap-2">
        <UButton
          :disabled="
            actionLocked || status.state === 'unconfigured' || status.job?.state === 'running'
          "
          @click="request('start')"
          >{{ t('eiStart') }}</UButton
        >
        <UButton
          v-if="status.job?.state === 'running'"
          variant="outline"
          :disabled="actionLocked"
          @click="request('pause')"
          >{{ t('eiPause') }}</UButton
        >
        <UButton
          v-if="status.job && ['paused', 'failed'].includes(status.job.state)"
          variant="outline"
          :disabled="actionLocked"
          @click="request('resume')"
          >{{ t('eiResume') }}</UButton
        >
        <UButton
          v-if="status.job && ['running', 'paused', 'failed'].includes(status.job.state)"
          color="warning"
          variant="outline"
          :disabled="actionLocked"
          @click="request('cancel')"
          >{{ t('eiCancelJob') }}</UButton
        >
      </div>
      <div v-if="pending" class="border border-warning rounded p-3 space-y-3">
        <p class="text-sm">
          {{
            pending === 'start' || pending === 'resume' ? t('eiChargeConfirm') : t('eiStopConfirm')
          }}
        </p>
        <UCheckbox
          v-if="pending === 'resume' && status.job?.requiresRetryConfirmation"
          v-model="confirmUnknown"
          :label="t('eiUnknownConfirm')"
        />
        <div class="flex gap-2">
          <UButton
            :disabled="
              actionLocked ||
              (pending === 'resume' && status.job?.requiresRetryConfirmation && !confirmUnknown)
            "
            @click="execute"
            >{{ t('eiConfirm') }}</UButton
          >
          <UButton variant="ghost" color="neutral" :disabled="busy" @click="pending = undefined">{{
            t('cancel')
          }}</UButton>
        </div>
      </div>
    </template>
  </section>
</template>
