<script setup lang="ts">
const { t } = useI18n(),
  { client } = useApi()
const {
  data: result,
  refresh,
  status,
} = await useAsyncData('status', () => client.GET('/api/status'), { server: false })
const { data: usage, refresh: refreshUsage } = await useAsyncData(
  'usage-overview',
  () => client.GET('/api/usage'),
  { server: false },
)
const usageSummary = computed(() =>
  (usage.value?.data ?? []).reduce(
    (sum, row) => ({
      calls: sum.calls + row.calls,
      priced: sum.priced + row.pricedCalls,
      cost: sum.cost + row.estimatedUsd,
    }),
    { calls: 0, priced: 0, cost: 0 },
  ),
)
const metrics = computed(() => [
  {
    label: t('enabledScenes'),
    value: result.value?.data?.enabledScenes ?? 0,
    icon: 'i-lucide-messages-square',
  },
  { label: t('models'), value: result.value?.data?.modelCount ?? 0, icon: 'i-lucide-brain' },
  {
    label: t('activeTasks'),
    value: result.value?.data?.activeTasks ?? 0,
    icon: 'i-lucide-activity',
  },
  {
    label: t('recentErrors'),
    value: result.value?.data?.recentErrors ?? 0,
    icon: 'i-lucide-circle-alert',
  },
])
async function refreshAll() {
  await Promise.all([refresh(), refreshUsage()])
}
</script>
<template>
  <div class="space-y-6">
    <div class="flex justify-between items-start">
      <div>
        <h1 class="text-2xl font-semibold">{{ t('overview') }}</h1>
        <p class="text-muted mt-1">{{ t('overviewHelp') }}</p>
      </div>
      <UButton
        variant="soft"
        icon="i-lucide-refresh-cw"
        :loading="status === 'pending'"
        @click="refreshAll"
        >{{ t('refresh') }}</UButton
      >
    </div>
    <UAlert v-if="result?.error" color="error" :title="result.error.error" />
    <div class="grid sm:grid-cols-2 xl:grid-cols-4 gap-4">
      <UCard v-for="metric in metrics" :key="metric.label"
        ><div class="flex justify-between text-muted">
          <span>{{ metric.label }}</span
          ><UIcon :name="metric.icon" class="text-primary size-5" />
        </div>
        <p class="text-3xl font-semibold mt-3">{{ metric.value }}</p></UCard
      >
    </div>
    <div class="grid lg:grid-cols-2 gap-5">
      <UCard
        ><template #header
          ><h2 class="font-semibold">{{ t('gettingStarted') }}</h2></template
        >
        <p class="text-muted mb-5">{{ t('setupHelp') }}</p>
        <UButton to="/models" icon="i-lucide-plus">{{ t('configureModels') }}</UButton></UCard
      ><UCard
        ><template #header
          ><h2 class="font-semibold">{{ t('runtime') }}</h2></template
        >
        <dl class="grid grid-cols-2 gap-3 text-sm">
          <dt class="text-muted">{{ t('uptime') }}</dt>
          <dd>{{ result?.data?.uptimeSeconds ?? 0 }} s</dd>
          <dt class="text-muted">SQLite vector</dt>
          <dd>{{ result?.data?.vectorVersion ?? '—' }}</dd>
          <dt class="text-muted">{{ t('runningModelRequests') }}</dt>
          <dd>{{ result?.data?.runningModelRequests ?? 0 }}</dd>
          <dt class="text-muted">{{ t('queuedModelRequests') }}</dt>
          <dd>{{ result?.data?.queuedModelRequests ?? 0 }}</dd>
          <dt class="text-muted">{{ t('version') }}</dt>
          <dd>{{ result?.data?.version ?? '—' }}</dd>
        </dl></UCard
      >
    </div>
    <UCard
      ><template #header
        ><h2 class="font-semibold">{{ t('usageTitle') }}</h2></template
      >
      <p class="text-sm text-muted">{{ t('usageEstimateHelp') }}</p>
      <p class="mt-3">
        {{ t('usageCalls') }}: {{ usageSummary.calls }} ·
        {{ t('usagePriced', { priced: usageSummary.priced, total: usageSummary.calls }) }}
      </p>
      <p class="mt-2">
        {{ t('usageCost') }}:
        {{ usageSummary.priced ? usageSummary.cost.toFixed(6) : t('usageUnknownCost') }}
      </p>
      <UButton to="/models" variant="soft" class="mt-3">{{ t('models') }}</UButton></UCard
    >
    <UAlert
      icon="i-lucide-shield-check"
      color="neutral"
      :title="t('privacyTitle')"
      :description="t('privacyHelp')"
    />
  </div>
</template>
