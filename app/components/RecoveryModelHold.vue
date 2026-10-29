<script setup lang="ts">
const { t } = useI18n()
const { authenticated, authenticatedFetch } = useApi()
const state = ref<{
  admissionHold: boolean
  restorationCoverage: 'none' | 'unknown' | 'manually_acknowledged'
  restorationId?: string
}>()
const busy = ref(false),
  error = ref(false),
  reviewed = ref(false)
let generation = 0,
  controller: AbortController | undefined,
  disposed = false
function clear() {
  generation++
  controller?.abort()
  state.value = undefined
  busy.value = false
  error.value = false
  reviewed.value = false
}
async function refresh() {
  clear()
  if (!authenticated.value || disposed) return
  const current = generation
  controller = new AbortController()
  busy.value = true
  try {
    const response = await authenticatedFetch('/api/budget', { signal: controller.signal })
    if (!response.ok) throw new Error('recovery_status_unavailable')
    const value = await response.json()
    if (current === generation && authenticated.value && !disposed) state.value = value
  } catch {
    if (current === generation && authenticated.value && !disposed) error.value = true
  } finally {
    if (current === generation) busy.value = false
  }
}
async function acknowledge() {
  if (!reviewed.value || !state.value?.admissionHold || !state.value.restorationId || busy.value)
    return
  if (!window.confirm(t('recoveryHoldConfirm'))) return
  const current = generation,
    restoreId = state.value.restorationId
  busy.value = true
  error.value = false
  try {
    const response = await authenticatedFetch('/api/budget/recovery/acknowledge', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        restoreId,
        acknowledgment: 'I_reviewed_accounting_and_accept_incomplete_history',
      }),
      signal: controller?.signal,
    })
    if (!response.ok) throw new Error('recovery_review_failed')
    if (current === generation && authenticated.value && !disposed) await refresh()
  } catch {
    if (current === generation && authenticated.value && !disposed) error.value = true
  } finally {
    if (current === generation) busy.value = false
  }
}
watch(authenticated, () => (authenticated.value ? void refresh() : clear()))
onMounted(() => void refresh())
onBeforeUnmount(() => {
  disposed = true
  clear()
})
</script>
<template>
  <section
    v-if="authenticated && (state?.restorationCoverage !== 'none' || error || busy)"
    class="rounded-lg border border-warning p-4 space-y-3"
    :aria-label="t('recoveryHoldTitle')"
  >
    <div class="flex justify-between items-center gap-2">
      <h3 class="font-semibold">{{ t('recoveryHoldTitle') }}</h3>
      <UButton size="sm" variant="outline" :disabled="busy" @click="refresh">{{
        t('refresh')
      }}</UButton>
    </div>
    <p v-if="busy" role="status">{{ t('uxLoading') }}</p>
    <p v-else-if="error" role="alert" class="text-error">{{ t('recoveryHoldError') }}</p>
    <template v-if="state && state.restorationCoverage !== 'none'">
      <p class="text-sm text-warning">
        {{ t(state.admissionHold ? 'recoveryHoldBlocked' : 'recoveryHoldAcknowledged') }}
      </p>
      <p class="text-sm">{{ t('recoveryHoldUnknown') }}</p>
      <template v-if="state.admissionHold">
        <label class="flex gap-2 text-sm items-start">
          <input v-model="reviewed" type="checkbox" :disabled="busy" class="mt-1" />
          <span>{{ t('recoveryHoldReview') }}</span>
        </label>
        <UButton color="warning" :disabled="busy || !reviewed" @click="acknowledge">{{
          t('recoveryHoldRelease')
        }}</UButton>
      </template>
    </template>
  </section>
</template>
