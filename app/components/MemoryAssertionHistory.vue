<script setup lang="ts">
const props = defineProps<{ sceneId: string; memoryId: string }>()
const { t, locale } = useI18n(),
  { authenticated, authenticatedFetch } = useApi()
type Revision = {
  version: number
  recordedAt: string | null
  validFrom: string | null
  validTo: string | null
  content: string
  status: string
  epistemicKind?: string
  validityCoverage: string
}
const expanded = ref(false),
  asOf = ref(''),
  validAt = ref(''),
  items = ref<Revision[]>([]),
  busy = ref(false),
  error = ref(false),
  limited = ref(false)
let controller: AbortController | undefined,
  epoch = 0,
  disposed = false
const date = (value: string | null) =>
  value && Number.isFinite(Date.parse(value))
    ? new Date(value).toLocaleString(locale.value)
    : t('mpUnknownTime')
function clear() {
  epoch++
  controller?.abort()
  items.value = []
  busy.value = false
  error.value = false
  limited.value = false
}
async function load() {
  clear()
  if (disposed || !expanded.value || !authenticated.value) return
  const current = epoch
  controller = new AbortController()
  busy.value = true
  try {
    const query = new URLSearchParams({ sceneId: props.sceneId, memoryId: props.memoryId })
    if (asOf.value) query.set('asOfRecorded', new Date(asOf.value).toISOString())
    if (validAt.value) query.set('validAt', new Date(validAt.value).toISOString())
    const response = await authenticatedFetch(`/api/memory-assertion-history?${query}`, {
      signal: controller.signal,
    })
    if (!response.ok) throw Error('unavailable')
    const data = await response.json()
    if (disposed || epoch !== current || !authenticated.value) return
    items.value = data.revisions
    limited.value = data.coverage.status === 'limited'
  } catch {
    if (epoch === current && authenticated.value) error.value = true
  } finally {
    if (epoch === current) busy.value = false
  }
}
watch(expanded, () => void load())
watch(
  () => [props.sceneId, props.memoryId, authenticated.value],
  () => {
    clear()
    expanded.value = false
    asOf.value = ''
    validAt.value = ''
  },
)
onBeforeUnmount(() => {
  disposed = true
  clear()
})
</script>
<template>
  <section class="rounded border border-default p-3 space-y-2">
    <UButton
      variant="outline"
      :disabled="!authenticated"
      :aria-expanded="expanded"
      @click="expanded = !expanded"
      >{{ t('cmHistoryTitle') }}</UButton
    >
    <div v-if="expanded" class="space-y-3">
      <p class="text-sm text-muted">{{ t('cmHistoryHelp') }}</p>
      <p class="text-xs text-muted">{{ t('cmSourceUnavailable') }}</p>
      <div class="grid sm:grid-cols-2 gap-2">
        <label class="text-sm"
          >{{ t('cmAsOf')
          }}<input
            v-model="asOf"
            type="datetime-local"
            class="block rounded border border-default bg-default p-2" /></label
        ><label class="text-sm"
          >{{ t('cmValidAt')
          }}<input
            v-model="validAt"
            type="datetime-local"
            class="block rounded border border-default bg-default p-2"
        /></label>
      </div>
      <p class="text-xs text-muted">{{ t('cmDateInputHelp') }}</p>
      <UButton :disabled="busy || !authenticated" @click="load">{{ t('cmQuery') }}</UButton>
      <p v-if="busy" role="status">{{ t('uxLoading') }}</p>
      <p v-if="error" role="alert" class="text-error">{{ t('cmHistoryUnavailable') }}</p>
      <p v-else-if="!busy && !items.length">{{ t('cmHistoryEmpty') }}</p>
      <p v-if="limited" class="text-xs text-muted">{{ t('cmHistoryLimited') }}</p>
      <article
        v-for="row in items"
        :key="row.version"
        class="border border-default rounded p-3 space-y-2"
      >
        <p class="text-xs">
          {{ t('cmVersion', { version: row.version }) }} · {{ t('mhStatus_' + row.status) }}
        </p>
        <p class="whitespace-pre-wrap break-words">{{ row.content }}</p>
        <p class="text-xs">{{ t('mpEpistemic_' + (row.epistemicKind || 'unknown')) }}</p>
        <dl class="grid sm:grid-cols-3 gap-2 text-xs">
          <div>
            <dt>{{ t('cmRecorded') }}</dt>
            <dd>{{ date(row.recordedAt) }}</dd>
          </div>
          <div>
            <dt>{{ t('mpValidFrom') }}</dt>
            <dd>{{ date(row.validFrom) }}</dd>
          </div>
          <div>
            <dt>{{ t('mpValidUntil') }}</dt>
            <dd>{{ date(row.validTo) }}</dd>
          </div>
        </dl>
        <p v-if="row.validityCoverage === 'unknown'" class="text-xs text-muted">
          {{ t('cmValidityUnknown') }}
        </p>
      </article>
    </div>
  </section>
</template>
