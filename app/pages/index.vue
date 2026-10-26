<script setup lang="ts">
import type { components } from '~/types/api'
const { t, locale } = useI18n(),
  { client } = useApi()
const status = ref<components['schemas']['Status']>(),
  scenes = ref<components['schemas']['Scene'][]>([]),
  logs = ref<components['schemas']['Log'][]>([]),
  config = ref<components['schemas']['ModelConfig']>(),
  usage = ref<components['schemas']['Usage'][]>([])
const loading = ref(false),
  error = ref(''),
  lastUpdated = ref('')
let disposed = false,
  controller: AbortController | undefined
const usageSummary = computed(() =>
  usage.value.reduce(
    (sum, row) => ({
      calls: sum.calls + row.calls,
      priced: sum.priced + row.pricedCalls,
      cost: sum.cost + row.estimatedUsd,
    }),
    { calls: 0, priced: 0, cost: 0 },
  ),
)
const roles = ['planner', 'thinker', 'learning', 'vision', 'embedding'] as const
const roleLabel = (role: string) => t('mcRole' + role[0]!.toUpperCase() + role.slice(1))
const routeModels = (role: string) =>
  config.value?.routes.find((route) => route.role === role)?.orderedModelIds ?? []
const modelName = (id?: string) =>
  config.value?.models.find((model) => model.id === id)?.displayName ?? t('uxNotAssigned')
const configured = computed(() =>
  ['planner', 'thinker'].every((role) =>
    routeModels(role).some((id) => config.value?.models.some((model) => model.id === id)),
  ),
)
const uptime = computed(() => {
  const seconds = status.value?.uptimeSeconds
  if (seconds === undefined) return '—'
  return seconds >= 3600
    ? t('ovUptimeHours', {
        hours: Math.floor(seconds / 3600),
        minutes: Math.floor((seconds % 3600) / 60),
      })
    : t('ovUptimeMinutes', { minutes: Math.floor(seconds / 60), seconds: seconds % 60 })
})
const metrics = computed(() => [
  { label: t('activeTasks'), value: status.value?.activeTasks },
  { label: t('runningModelRequests'), value: status.value?.runningModelRequests },
  { label: t('queuedModelRequests'), value: status.value?.queuedModelRequests },
  { label: t('recentErrors'), value: status.value?.recentErrors },
])
const adapterLabel = (value: string) =>
  ['connected', 'connecting', 'disconnected', 'disabled', 'error'].includes(value)
    ? t('ovAdapter_' + value)
    : value
const time = (at: string) =>
  new Date(at).toLocaleTimeString(locale.value, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
async function refreshAll() {
  if (loading.value || disposed) return
  loading.value = true
  error.value = ''
  controller = new AbortController()
  const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10000)])
  try {
    const [s, c, l, m, u] = await Promise.all([
      client.GET('/api/status', { signal }),
      client.GET('/api/scenes', { signal }),
      client.GET('/api/logs', { signal }),
      client.GET('/api/models', { signal }),
      client.GET('/api/usage', { signal }),
    ])
    if (disposed) return
    if (!s.data || !c.data || !l.data || !m.data || !u.data)
      throw new Error(
        s.error?.error ??
          c.error?.error ??
          l.error?.error ??
          m.error?.error ??
          u.error?.error ??
          t('networkError'),
      )
    status.value = s.data
    scenes.value = c.data
    logs.value = l.data
    config.value = m.data
    usage.value = u.data
    lastUpdated.value = new Date().toISOString()
  } catch (cause) {
    if (!disposed) error.value = cause instanceof Error ? cause.message : t('networkError')
  } finally {
    if (!disposed) loading.value = false
  }
}
onMounted(refreshAll)
onBeforeUnmount(() => {
  disposed = true
  controller?.abort()
})
</script>
<template>
  <div class="space-y-5">
    <header class="flex justify-between items-start gap-4">
      <div>
        <h1 class="page-heading">{{ t('overview') }}</h1>
        <p class="mt-2 text-sm text-muted">
          {{ status?.name ?? 'HumanEngine' }} · {{ t('ovIntro') }}
        </p>
      </div>
      <UButton
        class="shrink-0 whitespace-nowrap"
        color="neutral"
        variant="outline"
        icon="i-lucide-refresh-cw"
        :loading="loading"
        :disabled="loading"
        @click="refreshAll"
        >{{ t('refresh') }}</UButton
      >
    </header>
    <UAlert
      v-if="error"
      color="error"
      :title="error"
      :description="t(lastUpdated ? 'ovStale' : 'ovUnavailable')"
    />
    <p class="text-xs text-muted" aria-live="polite">
      {{
        lastUpdated
          ? t('ovUpdated', { time: time(lastUpdated) })
          : t(loading ? 'uxLoading' : 'ovNoSnapshot')
      }}
    </p>
    <template v-if="status">
      <section class="rounded-lg border border-default bg-default overflow-hidden">
        <header
          class="px-5 py-4 flex flex-wrap gap-3 justify-between items-center border-b border-default"
        >
          <h2 class="panel-heading">{{ t('ovRuntime') }}</h2>
          <span class="text-xs text-muted">{{ t('uptime') }} · {{ uptime }}</span>
        </header>
        <dl class="grid grid-cols-2 lg:grid-cols-4">
          <div
            v-for="metric in metrics"
            :key="metric.label"
            class="px-5 py-4 border-b lg:border-b-0 border-r last:border-r-0 border-default"
          >
            <dt class="text-xs text-muted">{{ metric.label }}</dt>
            <dd class="mt-2 text-xl font-semibold tabular-nums">{{ metric.value }}</dd>
          </div>
        </dl>
        <div
          class="border-t border-default px-5 py-3 flex flex-wrap items-center gap-x-6 gap-y-2 text-sm"
        >
          <span class="text-muted">{{ t('ovConnections') }}</span
          ><span
            v-for="adapter in status.adapters"
            :key="adapter.id"
            class="inline-flex flex-wrap gap-2 items-center"
            ><span class="break-all">{{ adapter.id }}</span
            ><UBadge
              :color="
                adapter.status === 'connected'
                  ? 'success'
                  : adapter.status === 'error'
                    ? 'error'
                    : 'neutral'
              "
              variant="subtle"
              >{{ adapterLabel(adapter.status) }}</UBadge
            ></span
          ><span v-if="!status.adapters.length" class="text-muted">{{ t('ovNoAdapters') }}</span
          ><NuxtLink to="/plugins" class="text-primary hover:underline ml-auto">{{
            t('pluginsTitle')
          }}</NuxtLink>
        </div>
      </section>
      <div class="grid lg:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)] gap-5 items-start">
        <section class="rounded-lg border border-default bg-default overflow-hidden">
          <header class="px-5 py-4 border-b border-default flex items-center justify-between gap-3">
            <div>
              <h2 class="panel-heading">{{ t('ovConversations') }}</h2>
              <p class="text-xs text-muted mt-1">
                {{
                  t('ovSceneCounts', { enabled: status.enabledScenes, total: status.sceneCount })
                }}
              </p>
            </div>
            <NuxtLink to="/scenes" class="text-sm text-primary shrink-0 hover:underline">{{
              t('uxViewAll')
            }}</NuxtLink>
          </header>
          <p v-if="!scenes.length" class="p-5 text-sm text-muted">{{ t('emptyScenes') }}</p>
          <ul class="divide-y divide-default">
            <li v-for="scene in scenes.slice(0, 5)" :key="scene.id" class="px-5 py-4">
              <div class="flex items-start gap-3">
                <UIcon
                  :name="scene.kind === 'group' ? 'i-lucide-users' : 'i-lucide-user-round'"
                  class="size-4 shrink-0 text-muted mt-1"
                />
                <div class="min-w-0 flex-1">
                  <p class="text-sm font-medium break-words">{{ scene.name }}</p>
                  <p class="text-xs text-muted mt-1">
                    {{ t(scene.kind === 'group' ? 'scGroup' : 'scPrivate') }}
                  </p>
                </div>
              </div>
              <div class="flex flex-wrap gap-x-5 gap-y-1 mt-2 pl-7 text-xs">
                <span :class="scene.participationEnabled ? 'text-default' : 'text-muted'">{{
                  t(scene.participationEnabled ? 'ovRepliesOn' : 'ovRepliesOff')
                }}</span
                ><span :class="scene.learningEnabled ? 'text-default' : 'text-muted'">{{
                  t(scene.learningEnabled ? 'ovLearningOn' : 'ovLearningOff')
                }}</span>
              </div>
            </li>
          </ul>
        </section>
        <section class="rounded-lg border border-default bg-default overflow-hidden">
          <header class="px-5 py-4 border-b border-default flex justify-between items-start gap-3">
            <div>
              <h2 class="panel-heading">{{ t('ovModels') }}</h2>
              <p class="text-xs text-muted mt-1">
                {{
                  t('ovModelCounts', { models: status.modelCount, providers: status.providerCount })
                }}
              </p>
            </div>
            <NuxtLink to="/models" class="text-sm text-primary shrink-0 hover:underline">{{
              t('configure')
            }}</NuxtLink>
          </header>
          <div
            class="px-5 py-3 border-b border-default text-xs leading-5"
            :class="configured ? 'text-muted' : 'text-warning'"
          >
            {{ t(configured ? 'ovConfiguredHelp' : 'ovNeedsSetup') }}
          </div>
          <dl class="divide-y divide-default">
            <div
              v-for="role in roles"
              :key="role"
              class="px-5 py-3 grid grid-cols-[100px_minmax(0,1fr)] gap-3"
            >
              <dt class="text-sm text-muted">{{ roleLabel(role) }}</dt>
              <dd class="text-sm min-w-0">
                <span class="block break-words font-medium">{{
                  modelName(routeModels(role)[0])
                }}</span
                ><span v-if="routeModels(role).length" class="block mt-1 text-xs text-muted">{{
                  t('ovPrimaryFallback', { count: Math.max(0, routeModels(role).length - 1) })
                }}</span>
              </dd>
            </div>
          </dl>
        </section>
      </div>
      <div class="grid lg:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)] gap-5 items-start">
        <section class="rounded-lg border border-default bg-default overflow-hidden">
          <header class="px-5 py-4 border-b border-default flex justify-between items-center">
            <h2 class="panel-heading">{{ t('ovEvents') }}</h2>
            <NuxtLink to="/logs" class="text-sm text-primary hover:underline">{{
              t('logs')
            }}</NuxtLink>
          </header>
          <p v-if="!logs.length" class="p-5 text-sm text-muted">{{ t('uxNoActivity') }}</p>
          <ul class="divide-y divide-default">
            <li
              v-for="log in logs.slice(0, 5)"
              :key="log.id"
              class="px-5 py-3 flex items-start gap-3"
            >
              <UIcon
                :name="
                  log.level === 'error'
                    ? 'i-lucide-circle-alert'
                    : log.level === 'warn'
                      ? 'i-lucide-triangle-alert'
                      : 'i-lucide-info'
                "
                :class="
                  log.level === 'error'
                    ? 'text-error'
                    : log.level === 'warn'
                      ? 'text-warning'
                      : 'text-info'
                "
                class="size-4 shrink-0 mt-1"
              />
              <div class="min-w-0 flex-1">
                <p class="font-mono text-xs break-all leading-5">{{ log.code }}</p>
                <p class="text-xs text-muted break-all mt-1">
                  {{ log.level.toUpperCase() }} · {{ log.module }}
                </p>
              </div>
              <time class="text-xs text-muted tabular-nums shrink-0">{{ time(log.at) }}</time>
            </li>
          </ul>
        </section>
        <section class="rounded-lg border border-default bg-default p-5 space-y-4">
          <div>
            <h2 class="panel-heading">{{ t('usageTitle') }}</h2>
            <p class="text-xs text-muted mt-2 leading-5">{{ t('ovUsageRange') }}</p>
          </div>
          <dl class="grid grid-cols-2 gap-4">
            <div>
              <dt class="text-xs text-muted">{{ t('usageCalls') }}</dt>
              <dd class="text-xl font-semibold tabular-nums mt-2">{{ usageSummary.calls }}</dd>
            </div>
            <div>
              <dt class="text-xs text-muted">{{ t('usageCost') }}</dt>
              <dd class="text-lg font-semibold tabular-nums mt-2">
                {{
                  usageSummary.priced ? '$' + usageSummary.cost.toFixed(4) : t('usageUnknownCost')
                }}
              </dd>
            </div>
          </dl>
          <p class="text-xs text-muted">
            {{ t('usagePriced', { priced: usageSummary.priced, total: usageSummary.calls }) }}
          </p>
          <details class="text-xs text-muted">
            <summary class="cursor-pointer">{{ t('uxEstimateDetails') }}</summary>
            <p class="mt-2 leading-6">{{ t('usageEstimateHelp') }}</p>
          </details>
        </section>
      </div>
      <details class="rounded-lg border border-default bg-default px-5 py-4 text-sm">
        <summary class="cursor-pointer font-medium">{{ t('ovTechnical') }}</summary>
        <dl class="grid sm:grid-cols-2 gap-4 mt-4 text-xs">
          <div>
            <dt class="text-muted">{{ t('version') }}</dt>
            <dd class="mt-1 font-mono">{{ status.version }}</dd>
          </div>
          <div>
            <dt class="text-muted">{{ t('ovVectorVersion') }}</dt>
            <dd class="mt-1 font-mono">{{ status.vectorVersion }}</dd>
          </div>
        </dl>
      </details>
      <p class="text-xs text-muted leading-5">{{ t('privacyHelp') }}</p>
    </template>
    <div v-else class="rounded-lg border border-default bg-default p-8 text-sm text-muted">
      {{ t(loading ? 'uxLoading' : 'ovUnavailable') }}
    </div>
  </div>
</template>
