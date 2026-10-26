<script setup lang="ts">
import type { components } from '~/types/api'
const { t, locale } = useI18n(),
  { client } = useApi()
const status = ref<components['schemas']['Status']>(),
  scenes = ref<components['schemas']['Scene'][]>([]),
  logs = ref<components['schemas']['Log'][]>([]),
  config = ref<components['schemas']['ModelConfig']>(),
  usage = ref<components['schemas']['Usage'][]>([]),
  loading = ref(false),
  error = ref('')
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
const configured = computed(() =>
  ['planner', 'thinker'].every((role) =>
    config.value?.routes.some((route) => route.role === role && route.orderedModelIds.length),
  ),
)
const metrics = computed(() => [
  {
    label: t('enabledScenes'),
    value: status.value?.enabledScenes,
    detail: t('uxSceneTotal', { count: status.value?.sceneCount ?? 0 }),
    icon: 'i-lucide-messages-square',
    to: '/scenes',
  },
  {
    label: t('modelInstances'),
    value: status.value?.modelCount,
    detail: t('uxProviderTotal', { count: status.value?.providerCount ?? 0 }),
    icon: 'i-lucide-brain',
    to: '/models',
  },
  {
    label: t('activeTasks'),
    value: status.value?.activeTasks,
    detail: t('uxQueuedTotal', { count: status.value?.queuedModelRequests ?? 0 }),
    icon: 'i-lucide-activity',
    to: '/chat',
  },
  {
    label: t('recentErrors'),
    value: status.value?.recentErrors,
    detail: t('uxInspectLogs'),
    icon: 'i-lucide-circle-alert',
    to: '/logs',
  },
])
const uptime = computed(() => {
  const seconds = status.value?.uptimeSeconds
  if (seconds === undefined) return '—'
  return seconds >= 3600
    ? `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`
    : `${Math.floor(seconds / 60)}m ${seconds % 60}s`
})
function roleLabel(role: string) {
  return t('uxRole' + role[0]!.toUpperCase() + role.slice(1))
}
async function refreshAll() {
  if (loading.value) return
  loading.value = true
  error.value = ''
  try {
    const [s, c, l, m, u] = await Promise.all([
      client.GET('/api/status'),
      client.GET('/api/scenes'),
      client.GET('/api/logs'),
      client.GET('/api/models'),
      client.GET('/api/usage'),
    ])
    if (s.data) status.value = s.data
    if (c.data) scenes.value = c.data
    if (l.data) logs.value = l.data
    if (m.data) config.value = m.data
    if (u.data) usage.value = u.data
    error.value =
      s.error?.error ?? c.error?.error ?? l.error?.error ?? m.error?.error ?? u.error?.error ?? ''
  } catch {
    error.value = t('networkError')
  } finally {
    loading.value = false
  }
}
onMounted(refreshAll)
</script>
<template>
  <div class="space-y-4">
    <header class="flex items-center justify-between gap-4">
      <div>
        <h1 class="page-heading">{{ t('overview') }}</h1>
        <p class="text-sm text-muted mt-1">
          {{ status?.name ?? 'HumanEngine' }} <span class="mx-1.5 text-dimmed">/</span>
          {{ t('uxOverviewHelp') }}
        </p>
      </div>
      <UButton
        color="neutral"
        variant="outline"
        icon="i-lucide-refresh-cw"
        :loading="loading"
        @click="refreshAll"
        >{{ t('refresh') }}</UButton
      >
    </header>
    <UAlert v-if="error" color="error" :title="error" />
    <div
      v-if="config && !configured"
      class="surface flex flex-wrap items-center justify-between gap-3 px-4 py-2.5 border-primary/25 bg-primary/5"
    >
      <span class="text-sm flex items-center gap-2"
        ><UIcon name="i-lucide-route" class="size-4 text-primary" />{{ t('uxSetupRoutes') }}</span
      ><UButton to="/models" size="sm" variant="soft" trailing-icon="i-lucide-arrow-right">{{
        t('configureModels')
      }}</UButton>
    </div>
    <div class="grid grid-cols-2 lg:grid-cols-4 gap-3">
      <NuxtLink
        v-for="metric in metrics"
        :key="metric.label"
        :to="metric.to"
        class="surface px-4 py-3.5 transition-colors hover:border-primary/40 focus-visible:outline-2 focus-visible:outline-primary"
      >
        <div class="flex items-center justify-between gap-2 text-xs text-muted">
          <span>{{ metric.label }}</span
          ><UIcon :name="metric.icon" class="size-4 text-primary" />
        </div>
        <div class="flex items-end justify-between gap-2 mt-2">
          <strong class="text-2xl font-semibold tabular text-highlighted">{{
            metric.value ?? '—'
          }}</strong
          ><span class="text-[11px] text-muted text-right">{{ metric.detail }}</span>
        </div>
      </NuxtLink>
    </div>
    <div class="grid lg:grid-cols-3 gap-4 items-start">
      <div class="lg:col-span-2 space-y-4">
        <section class="surface overflow-hidden">
          <header class="px-4 py-3 flex items-center justify-between border-b border-default">
            <h2 class="panel-heading">{{ t('scenes') }}</h2>
            <UButton
              to="/scenes"
              size="xs"
              color="neutral"
              variant="ghost"
              trailing-icon="i-lucide-arrow-up-right"
              >{{ t('uxViewAll') }}</UButton
            >
          </header>
          <div v-if="!scenes.length" class="px-4 py-8 text-sm text-muted">
            {{ loading ? t('uxLoading') : t('emptyScenes') }}
          </div>
          <div v-else class="divide-y divide-default">
            <div
              v-for="scene in scenes.slice(0, 5)"
              :key="scene.id"
              class="flex items-center gap-3 px-4 py-3 min-h-14"
            >
              <span
                class="size-8 shrink-0 rounded-md bg-elevated grid place-items-center text-muted"
                ><UIcon
                  :name="scene.kind === 'group' ? 'i-lucide-users' : 'i-lucide-user-round'"
                  class="size-4"
              /></span>
              <div class="flex-1 min-w-0">
                <p
                  class="text-sm font-medium text-highlighted line-clamp-2 break-words"
                  :title="scene.name"
                >
                  {{ scene.name }}
                </p>
                <p class="text-xs text-muted truncate">{{ scene.adapterId }}</p>
              </div>
              <div class="flex flex-wrap justify-end gap-1.5">
                <UBadge
                  :color="scene.participationEnabled ? 'primary' : 'neutral'"
                  variant="subtle"
                  size="sm"
                  >{{ t(scene.participationEnabled ? 'participation' : 'uxPaused') }}</UBadge
                ><UBadge v-if="scene.learningEnabled" variant="soft" color="neutral" size="sm">{{
                  t('learning')
                }}</UBadge>
              </div>
            </div>
          </div>
        </section>
        <section class="surface overflow-hidden">
          <header class="px-4 py-3 flex items-center justify-between border-b border-default">
            <h2 class="panel-heading">{{ t('uxRecentActivity') }}</h2>
            <UButton
              to="/logs"
              size="xs"
              color="neutral"
              variant="ghost"
              trailing-icon="i-lucide-arrow-up-right"
              >{{ t('logs') }}</UButton
            >
          </header>
          <p v-if="!logs.length" class="px-4 py-8 text-sm text-muted">{{ t('uxNoActivity') }}</p>
          <div
            v-for="log in logs.slice(0, 5)"
            :key="log.id"
            class="flex items-start gap-3 px-4 py-3 border-b last:border-0 border-default"
          >
            <UIcon
              :name="log.level === 'error' ? 'i-lucide-circle-alert' : 'i-lucide-circle-check'"
              :class="log.level === 'error' ? 'text-error' : 'text-muted'"
              class="size-4 shrink-0 mt-0.5"
            />
            <div class="min-w-0 flex-1">
              <p class="text-sm font-medium break-words">{{ log.code }}</p>
              <p class="text-xs text-muted">{{ log.module }}</p>
            </div>
            <time class="text-xs tabular text-muted whitespace-nowrap">{{
              new Date(log.at).toLocaleTimeString(locale, { hour: '2-digit', minute: '2-digit' })
            }}</time>
          </div>
        </section>
      </div>
      <div class="space-y-4">
        <section class="surface p-4">
          <h2 class="panel-heading mb-3">{{ t('runtime') }}</h2>
          <dl class="space-y-2.5 text-sm">
            <div class="flex justify-between gap-3">
              <dt class="text-muted">{{ t('uptime') }}</dt>
              <dd class="tabular">{{ uptime }}</dd>
            </div>
            <div class="flex justify-between gap-3">
              <dt class="text-muted">{{ t('runningModelRequests') }}</dt>
              <dd class="tabular">{{ status?.runningModelRequests ?? '—' }}</dd>
            </div>
            <div class="flex justify-between gap-3">
              <dt class="text-muted">{{ t('queuedModelRequests') }}</dt>
              <dd class="tabular">{{ status?.queuedModelRequests ?? '—' }}</dd>
            </div>
            <div
              v-for="adapter in status?.adapters ?? []"
              :key="adapter.id"
              class="flex justify-between gap-3"
            >
              <dt class="text-muted truncate" :title="adapter.id">{{ adapter.id }}</dt>
              <dd>
                <UBadge color="neutral" variant="subtle" size="sm">{{ adapter.status }}</UBadge>
              </dd>
            </div>
            <div class="flex justify-between gap-3 border-t border-default pt-2.5">
              <dt class="text-muted">SQLite vector</dt>
              <dd class="text-xs tabular">{{ status?.vectorVersion ?? '—' }}</dd>
            </div>
            <div class="flex justify-between gap-3">
              <dt class="text-muted">{{ t('version') }}</dt>
              <dd class="text-xs tabular">{{ status?.version ?? '—' }}</dd>
            </div>
          </dl>
        </section>
        <section class="surface p-4">
          <header class="flex justify-between items-center mb-3">
            <h2 class="panel-heading">{{ t('roleRoutes') }}</h2>
            <UButton
              to="/models"
              icon="i-lucide-arrow-up-right"
              :aria-label="t('models')"
              variant="ghost"
              color="neutral"
              size="xs"
            />
          </header>
          <div
            v-for="role in ['planner', 'thinker', 'learning', 'vision', 'embedding']"
            :key="role"
            class="flex justify-between items-baseline gap-3 py-2 border-b last:border-0 border-default text-xs"
          >
            <span class="text-muted shrink-0">{{ roleLabel(role) }}</span
            ><span
              class="font-medium text-right truncate"
              :title="
                config?.models.find(
                  (m) => m.id === config?.routes.find((r) => r.role === role)?.orderedModelIds[0],
                )?.displayName
              "
              >{{
                config?.models.find(
                  (m) => m.id === config?.routes.find((r) => r.role === role)?.orderedModelIds[0],
                )?.displayName ?? t('uxNotAssigned')
              }}</span
            >
          </div>
        </section>
        <section class="surface p-4">
          <h2 class="panel-heading">{{ t('usageTitle') }}</h2>
          <div class="grid grid-cols-2 gap-3 mt-3">
            <div>
              <p class="text-xs text-muted">{{ t('usageCalls') }}</p>
              <p class="text-xl tabular font-semibold mt-1">{{ usageSummary.calls }}</p>
            </div>
            <div>
              <p class="text-xs text-muted">{{ t('usageCost') }}</p>
              <p class="text-sm tabular font-semibold mt-2">
                {{
                  usageSummary.priced ? '$' + usageSummary.cost.toFixed(4) : t('usageUnknownCost')
                }}
              </p>
            </div>
          </div>
          <p class="text-xs text-muted mt-3">
            {{ t('usagePriced', { priced: usageSummary.priced, total: usageSummary.calls }) }}
          </p>
          <details class="mt-2 text-xs text-muted">
            <summary class="cursor-pointer">{{ t('uxEstimateDetails') }}</summary>
            <p class="mt-2 leading-relaxed">{{ t('usageEstimateHelp') }}</p>
          </details>
        </section>
      </div>
    </div>
    <p class="flex items-start gap-2 text-xs text-muted px-1">
      <UIcon name="i-lucide-shield-check" class="size-3.5 shrink-0 mt-0.5" />{{ t('privacyHelp') }}
    </p>
  </div>
</template>
