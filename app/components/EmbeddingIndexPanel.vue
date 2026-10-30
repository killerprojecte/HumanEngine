<script setup lang="ts">
import type { components } from '~/types/api'
import {
  embeddingIndexActions,
  type EmbeddingIndexAction as Action,
} from '~/utils/embedding-index-actions'
const props = defineProps<{ configBusy: boolean; compact?: boolean }>()
const { t } = useI18n()
const { client } = useApi()
type Status = components['schemas']['EmbeddingIndexStatus']
const status = ref<Status>(),
  busy = ref(false),
  error = ref(''),
  uncertain = ref(false)
const confirmationAction = ref<Action>('start')
const pending = ref<Action>(),
  confirmUnknown = ref(false),
  detailsOpen = ref(false)
const actionLocked = computed(() => busy.value || props.configBusy || uncertain.value)
const availableActions = computed(() => embeddingIndexActions(status.value))
const activeJob = computed(() =>
  status.value?.job && ['running', 'paused', 'failed'].includes(status.value.job.state)
    ? status.value.job
    : undefined,
)
const actionLabels: Record<Action, string> = {
  start: 'eiStart',
  pause: 'eiPause',
  resume: 'eiResume',
  cancel: 'eiCancelJob',
}
const actionIcons: Record<Action, string> = {
  start: 'i-lucide-refresh-cw',
  pause: 'i-lucide-pause',
  resume: 'i-lucide-play',
  cancel: 'i-lucide-circle-x',
}
const menuItems = computed(() =>
  [
    availableActions.value.map((action) => ({
      label: t(actionLabels[action]),
      icon: actionIcons[action],
      disabled: actionLocked.value,
      onSelect: () => request(action),
    })),
    [
      {
        label: t('eiDetails'),
        icon: 'i-lucide-info',
        disabled: !status.value,
        onSelect: () => {
          detailsOpen.value = true
        },
      },
      {
        label: t('refresh'),
        icon: 'i-lucide-refresh-cw',
        disabled: busy.value,
        onSelect: () => {
          void refresh()
        },
      },
    ],
  ].filter((group) => group.length),
)
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
    if (!disposed) {
      error.value = t('networkError')
      uncertain.value = true
    }
  } finally {
    busy.value = false
    if (refreshQueued && !disposed) void refresh()
  }
}
function request(action: Action) {
  if (actionLocked.value || !availableActions.value.includes(action)) return
  confirmationAction.value = action
  pending.value = action
  confirmUnknown.value = false
}
async function execute() {
  const action = pending.value
  if (
    !action ||
    actionLocked.value ||
    !availableActions.value.includes(action) ||
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
  <section
    :class="
      props.compact
        ? 'inline-flex min-w-0 items-center'
        : 'rounded-lg border border-default bg-default'
    "
    :aria-label="t('eiTitle')"
  >
    <div
      :class="
        props.compact
          ? 'inline-flex items-center'
          : 'flex flex-wrap items-center gap-x-4 gap-y-2 px-3 py-2.5'
      "
    >
      <div
        v-if="!props.compact"
        class="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1.5"
      >
        <h2 class="text-sm font-semibold">{{ t('eiTitle') }}</h2>
        <UBadge
          v-if="status"
          size="sm"
          :color="status.state === 'needs_rebuild' ? 'warning' : 'neutral'"
          variant="subtle"
          >{{ t(`eiState_${status.state}`) }}</UBadge
        >
        <span v-else-if="busy" role="status" class="text-xs text-muted">{{ t('uxLoading') }}</span>
        <span v-if="status" class="text-xs text-muted tabular">{{
          t('eiCoverage', status.coverage)
        }}</span>
      </div>
      <UDropdownMenu :items="menuItems" :content="{ align: 'end' }">
        <template v-if="props.compact && error" #content-top
          ><p role="alert" class="max-w-64 p-2 text-xs text-warning">{{ error }}</p></template
        >
        <UButton
          :color="
            props.compact && (status?.state === 'needs_rebuild' || error) ? 'warning' : 'neutral'
          "
          :variant="props.compact ? 'outline' : 'ghost'"
          :size="props.compact ? 'md' : 'sm'"
          :ui="props.compact ? { leadingIcon: 'hidden sm:block' } : {}"
          :icon="
            props.compact ? (error ? 'i-lucide-circle-alert' : 'i-lucide-database') : undefined
          "
          trailing-icon="i-lucide-chevron-down"
          :loading="busy"
          :aria-label="`${t('eiTitle')} · ${t('mcActions')}`"
          ><template v-if="props.compact"
            ><span>{{
              activeJob
                ? t(`eiJob_${activeJob.state}`)
                : status?.state === 'needs_rebuild'
                  ? t('eiNeedsAction')
                  : t('eiIndexMenu')
            }}</span
            ><span v-if="activeJob" class="text-xs tabular"
              >{{ activeJob.processed }}/{{ activeJob.total }}</span
            ></template
          ><template v-else>{{ t('mcActions') }}</template></UButton
        >
        <template v-if="props.compact && activeJob?.state === 'running'" #content-bottom
          ><div class="px-2 py-1">
            <UProgress
              :model-value="activeJob.total ? activeJob.processed : null"
              :max="activeJob.total || 100"
              :aria-label="t('eiJob_running')"
              size="xs"
            /></div
        ></template>
      </UDropdownMenu>
    </div>
    <div
      v-if="activeJob && !props.compact"
      class="flex flex-wrap items-center gap-x-3 gap-y-1.5 border-t border-default px-3 py-2 text-xs"
    >
      <span class="tabular" role="status">{{
        t('eiJob', {
          state: t(`eiJob_${activeJob.state}`),
          processed: activeJob.processed,
          total: activeJob.total,
        })
      }}</span>
      <UProgress
        v-if="activeJob.state === 'running'"
        :model-value="activeJob.total ? activeJob.processed : null"
        :max="activeJob.total || 100"
        :aria-label="t('eiJob_running')"
        size="xs"
        class="w-28"
      />
      <span v-if="activeJob.error" class="min-w-0 break-words text-warning">{{
        activeJob.error
      }}</span>
      <span
        v-if="activeJob.requiresRetryConfirmation && activeJob.state !== 'running'"
        class="text-warning"
        >{{ t('eiUnknownWarning') }}</span
      >
    </div>
    <div
      v-if="error && !props.compact"
      class="flex flex-wrap items-center gap-2 border-t border-default px-3 py-2 text-xs text-warning"
      role="alert"
    >
      <span class="min-w-0 flex-1">{{ error }}</span>
      <UButton size="xs" color="neutral" variant="link" :disabled="busy" @click="refresh">{{
        t('refresh')
      }}</UButton>
    </div>
    <UModal
      v-model:open="detailsOpen"
      :title="t('eiDetails')"
      :description="t('eiHelp')"
      :ui="{ content: 'max-w-xl', body: 'space-y-4 overflow-y-auto' }"
    >
      <template #body>
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
          <div v-if="status.job" class="space-y-2 border-t border-default pt-3 text-xs">
            <p>
              {{
                t('eiJob', {
                  state: t(`eiJob_${status.job.state}`),
                  processed: status.job.processed,
                  total: status.job.total,
                })
              }}
            </p>
            <p class="break-all">
              {{ t('eiStaging') }}: {{ status.job.id }} ·
              {{ status.job.dimensions ?? t('eiAwaitingDimensions') }}
            </p>
            <p v-if="status.job.error" class="break-all text-warning">{{ status.job.error }}</p>
          </div>
        </template>
      </template>
    </UModal>
    <UModal
      :open="!!pending"
      :title="t(actionLabels[confirmationAction])"
      :description="
        confirmationAction === 'start'
          ? `${t('eiChargeConfirm')} ${t('eiReplacePaused')}`
          : confirmationAction === 'resume'
            ? t('eiChargeConfirm')
            : t('eiStopConfirm')
      "
      :dismissible="!busy"
      :close="!busy"
      :ui="{ content: 'max-w-lg', footer: 'justify-end' }"
      @update:open="
        (open) => {
          if (!open && !busy) pending = undefined
        }
      "
    >
      <template v-if="pending === 'resume' && status?.job?.requiresRetryConfirmation" #body>
        <UCheckbox v-model="confirmUnknown" :label="t('eiUnknownConfirm')" />
      </template>
      <template #footer>
        <UButton variant="ghost" color="neutral" :disabled="busy" @click="pending = undefined">{{
          t('cancel')
        }}</UButton>
        <UButton
          :disabled="
            actionLocked ||
            (pending === 'resume' && status?.job?.requiresRetryConfirmation && !confirmUnknown)
          "
          @click="execute"
          >{{ t(confirmationAction === 'start' ? 'eiConfirmRebuild' : 'eiConfirm') }}</UButton
        >
      </template>
    </UModal>
  </section>
</template>
