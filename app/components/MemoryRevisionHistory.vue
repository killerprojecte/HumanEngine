<script setup lang="ts">
const props = defineProps<{ sceneId: string; memoryId: string }>()
const { t, locale } = useI18n()
const { authenticatedFetch, authenticated } = useApi()
type Revision = {
  version: number
  recordedAt: string | null
  validFrom: string | null
  validTo: string | null
  content: string
  status: string
  kind: string
  epistemicKind?: string
  occurredAt?: string
  origin: string
  sourceBodyCoverage?: string
  parentBinding?: string
}
const expanded = ref(false),
  busy = ref(false),
  error = ref(false)
const revisions = ref<Revision[]>([])
const limited = ref(false)
let controller: AbortController | undefined,
  epoch = 0,
  disposed = false
function clear() {
  ++epoch
  controller?.abort()
  busy.value = false
  error.value = false
  revisions.value = []
  limited.value = false
}
async function load() {
  clear()
  if (!expanded.value || !authenticated.value || !props.sceneId || !props.memoryId || disposed)
    return
  const current = epoch
  controller = new AbortController()
  busy.value = true
  try {
    const response = await authenticatedFetch(
      `/api/memory-history?${new URLSearchParams({ sceneId: props.sceneId, id: props.memoryId })}`,
      { signal: controller.signal },
    )
    if (!response.ok) throw new Error('history_unavailable')
    const result = (await response.json()) as { revisions: Revision[]; coverage?: string }
    if (!disposed && current === epoch && authenticated.value && expanded.value) {
      revisions.value = result.revisions
      limited.value = result.coverage === 'limited'
    }
  } catch {
    if (!disposed && current === epoch && authenticated.value && expanded.value) error.value = true
  } finally {
    if (current === epoch) busy.value = false
  }
}
watch(expanded, () => (expanded.value ? void load() : clear()))
watch(
  () => [props.sceneId, props.memoryId],
  () => {
    expanded.value = false
    clear()
  },
)
watch(authenticated, () => {
  expanded.value = false
  clear()
})
onBeforeUnmount(() => {
  disposed = true
  clear()
})
function date(value?: string | null) {
  if (!value || !Number.isFinite(Date.parse(value))) return t('mhUnknownTime')
  return new Date(value).toLocaleString(locale.value)
}
</script>
<template>
  <section class="space-y-2">
    <UButton
      variant="link"
      size="sm"
      :aria-expanded="expanded"
      :disabled="!authenticated"
      @click="expanded = !expanded"
    >
      {{ t(expanded ? 'mhCollapse' : 'mhExpand') }}
    </UButton>
    <div v-if="expanded" class="rounded-md border border-default p-3 space-y-3">
      <p class="text-xs text-muted">{{ t('mhHelp') }}</p>
      <p class="text-xs text-muted">{{ t('mhSourceBodyUnavailable') }}</p>
      <p v-if="limited" class="text-xs text-warning">{{ t('mhLimited') }}</p>
      <p v-if="busy" role="status" class="text-sm">{{ t('uxLoading') }}</p>
      <div v-else-if="error" role="alert" class="text-sm text-error">
        {{ t('mhUnavailable') }}
        <UButton variant="link" size="sm" @click="load">{{ t('mhRetry') }}</UButton>
      </div>
      <p v-else-if="!revisions.length" class="text-sm text-muted">{{ t('mhEmpty') }}</p>
      <ol v-else class="space-y-3">
        <li
          v-for="item in revisions"
          :key="item.version"
          class="border-t border-default pt-3 first:border-t-0 first:pt-0 space-y-2"
        >
          <div class="flex flex-wrap gap-2 text-xs text-muted">
            <span>{{ t('mhVersion', { version: item.version }) }}</span>
            <span>{{ t('memoryKind_' + item.kind) }}</span>
            <span>{{ t('mhStatus_' + item.status) }}</span>
            <span>{{ t('mhOrigin_' + item.origin) }}</span>
            <span v-if="item.parentBinding && item.parentBinding !== 'none'">{{
              t('mhParent_' + item.parentBinding)
            }}</span>
            <span>{{
              t(item.epistemicKind ? 'mhEpistemic_' + item.epistemicKind : 'mhEpistemicUnknown')
            }}</span>
          </div>
          <p class="whitespace-pre-wrap break-words text-sm">{{ item.content }}</p>
          <dl class="grid sm:grid-cols-2 gap-2 text-xs">
            <div>
              <dt class="text-muted">{{ t('mhRecorded') }}</dt>
              <dd>{{ date(item.recordedAt) }}</dd>
            </div>
            <div>
              <dt class="text-muted">{{ t('mhOccurred') }}</dt>
              <dd>{{ date(item.occurredAt) }}</dd>
            </div>
            <div>
              <dt class="text-muted">{{ t('mhValidFrom') }}</dt>
              <dd>{{ date(item.validFrom) }}</dd>
            </div>
            <div>
              <dt class="text-muted">{{ t('mhValidTo') }}</dt>
              <dd>{{ date(item.validTo) }}</dd>
            </div>
          </dl>
        </li>
      </ol>
    </div>
  </section>
</template>
