<script setup lang="ts">
const { t } = useI18n(),
  { client } = useApi()
const {
  data: result,
  refresh,
  status,
} = await useAsyncData('logs', () => client.GET('/api/logs'), { server: false })
const { data: diagnostics, refresh: refreshDiagnostics } = await useAsyncData(
  'diagnostics',
  () => client.GET('/api/diagnostics'),
  { server: false },
)
async function refreshAll() {
  await Promise.all([refresh(), refreshDiagnostics()])
}
</script>
<template>
  <div class="space-y-6">
    <UCard v-if="diagnostics?.data?.length"
      ><template #header
        ><h2 class="font-semibold">{{ t('visibleDiagnostics') }}</h2></template
      >
      <p class="text-muted text-sm mb-4">{{ t('visibleDiagnosticsHelp') }}</p>
      <details v-for="entry in diagnostics.data" :key="entry.id" class="mb-4">
        <summary>
          {{ entry.role }} · {{ entry.modelId }} · {{ new Date(entry.at).toLocaleString() }}
        </summary>
        <h3 class="mt-3 font-medium">{{ t('modelInput') }}</h3>
        <pre class="text-xs whitespace-pre-wrap break-all bg-muted p-3 rounded-lg">{{
          entry.input
        }}</pre>
        <h3 class="mt-3 font-medium">{{ t('modelOutput') }}</h3>
        <pre class="text-xs whitespace-pre-wrap break-all bg-muted p-3 rounded-lg">{{
          entry.output
        }}</pre>
      </details></UCard
    >
    <div class="flex justify-between">
      <h1 class="text-2xl font-semibold">{{ t('logs') }}</h1>
      <UButton
        variant="soft"
        icon="i-lucide-refresh-cw"
        :loading="status === 'pending'"
        @click="refreshAll"
        >{{ t('refresh') }}</UButton
      >
    </div>
    <p class="text-muted">{{ t('logsHelp') }}</p>
    <UCard
      ><div v-if="!result?.data?.length" class="text-muted">{{ t('emptyLogs') }}</div>
      <div
        v-for="log in result?.data ?? []"
        :key="log.id"
        class="py-3 border-b border-default last:border-0 flex flex-wrap items-center gap-3 text-sm"
      >
        <UBadge
          :color="log.level === 'error' ? 'error' : log.level === 'warn' ? 'warning' : 'neutral'"
          >{{ log.level }}</UBadge
        ><time class="text-muted">{{ new Date(log.at).toLocaleString() }}</time
        ><span>{{ log.module }}</span
        ><code>{{ log.code }}</code
        ><span v-if="log.modelId">{{ log.modelId }} · {{ log.durationMs }} ms</span
        ><span v-if="log.sceneId" class="text-muted">{{ log.sceneId }}</span>
      </div></UCard
    >
  </div>
</template>
