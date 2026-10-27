<script setup lang="ts">
import type { components } from '~/types/api'
type Log = components['schemas']['Log']
type Diagnostic = components['schemas']['Diagnostic']
const { t, locale } = useI18n(),
  { client } = useApi()
const latest = ref<Log[]>([])
const lastFetched = ref(''),
  logError = ref('')
const roleLabel = (role: string) =>
  ['planner', 'thinker', 'learning', 'vision', 'embedding'].includes(role)
    ? t('mcRole' + role[0]!.toUpperCase() + role.slice(1))
    : role
const newCount = computed(() => {
  const shownIds = new Set(logs.value.map((log) => log.id))
  return latest.value.filter((log) => !shownIds.has(log.id)).length
})
const logs = ref<Log[]>([]),
  diagnostics = ref<Diagnostic[]>([])
const busy = ref(false),
  live = ref(true),
  following = ref(true),
  error = ref('')
const search = ref(''),
  level = ref('all'),
  moduleFilter = ref('*')
const consolePanel = ref<HTMLElement>(),
  expanded = ref(new Set<string>())
const modules = computed(() => [
  { label: t('uxLogAllModules'), value: '*' },
  ...[...new Set(logs.value.map((log) => log.module))]
    .sort()
    .map((value) => ({ label: value, value })),
])
const levels = computed(() => [
  { label: t('uxLogAllLevels'), value: 'all' },
  ...['info', 'warn', 'error'].map((value) => ({ label: value.toUpperCase(), value })),
])
const filtered = computed(() => {
  const query = search.value.trim().toLowerCase()
  return logs.value.filter(
    (log) =>
      (level.value === 'all' || log.level === level.value) &&
      (moduleFilter.value === '*' || log.module === moduleFilter.value) &&
      (!query ||
        [
          log.at,
          log.level,
          log.module,
          log.code,
          log.operation,
          log.phase,
          log.errorType,
          log.requestId,
          log.systemCode,
          log.httpStatus,
          log.sceneId,
          log.turnId,
          log.modelId,
          log.providerId,
        ]
          .filter(Boolean)
          .join(' ')
          .toLowerCase()
          .includes(query)),
  )
})
let timer: ReturnType<typeof setTimeout> | undefined
let controller: AbortController | undefined
let generation = 0,
  disposed = false
async function jumpToLatest() {
  live.value = true
  following.value = true
  logs.value = latest.value
  await nextTick()
  if (consolePanel.value) consolePanel.value.scrollTop = consolePanel.value.scrollHeight
}
function onScroll() {
  const panel = consolePanel.value
  if (panel && panel.scrollHeight - panel.scrollTop - panel.clientHeight > 100) {
    following.value = false
    live.value = false
  }
}
async function refresh(force = false) {
  if (busy.value || disposed) return
  clearTimeout(timer)
  const current = ++generation
  controller = new AbortController()
  busy.value = true
  const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10000)])
  try {
    const [result, detail] = await Promise.all([
      client.GET('/api/logs', { signal }),
      client.GET('/api/diagnostics', { signal }),
    ])
    if (disposed || current !== generation) return
    error.value = ''
    if (result.data) {
      lastFetched.value = new Date().toISOString()
      logError.value = ''
      // The API returns newest first. Keep one bounded snapshot, oldest at the top.
      latest.value = result.data.slice(0, 500).reverse()
      if (live.value || force) logs.value = latest.value
      expanded.value = new Set(
        [...expanded.value].filter((id) => logs.value.some((log) => log.id === id)),
      )
    } else {
      logError.value = result.error?.error ?? t('networkError')
      error.value = logError.value
    }
    // Never retain old model I/O when the server clears it or verification fails.
    diagnostics.value = detail.data?.slice(0, 20) ?? []
    if (!detail.data) error.value ||= detail.error?.error ?? t('networkError')
    if (following.value) await jumpToLatest()
  } catch {
    if (current === generation && !disposed) {
      error.value = t('networkError')
      logError.value = error.value
      diagnostics.value = []
    }
  } finally {
    if (current === generation) {
      busy.value = false
      controller = undefined
      if (!disposed) timer = setTimeout(() => void refresh(), 2500)
    }
  }
}
function toggleLive() {
  if (live.value) {
    live.value = false
    following.value = false
  } else void jumpToLatest()
}
function toggleContext(id: string) {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}
function context(log: Log) {
  return Object.fromEntries(
    Object.entries({
      id: log.id,
      operation: log.operation,
      phase: log.phase,
      errorType: log.errorType,
      stackFrames: log.stackFrames,
      causes: log.causes,
      requestId: log.requestId,
      systemCode: log.systemCode,
      httpStatus: log.httpStatus,
      sceneId: log.sceneId,
      turnId: log.turnId,
      modelId: log.modelId,
      providerId: log.providerId,
      durationMs: log.durationMs,
    }).filter(([, value]) => value !== undefined),
  )
}
const timestamp = (at: string) =>
  new Date(at).toLocaleString(locale.value, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  })
watch([search, level, moduleFilter], () => {
  if (following.value) void jumpToLatest()
})
onMounted(() => void refresh())
onBeforeUnmount(() => {
  disposed = true
  ++generation
  clearTimeout(timer)
  controller?.abort()
  diagnostics.value = []
})
</script>
<template>
  <div class="space-y-4">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h1 class="page-heading">{{ t('logs') }}</h1>
        <p class="mt-1 text-sm text-muted">{{ t('logsHelp') }}</p>
      </div>
    </header>
    <UAlert v-if="error" color="error" :title="error" />
    <section
      class="overflow-hidden rounded-lg border border-default"
      :aria-label="t('uxLogConsole')"
    >
      <div class="flex flex-wrap items-center gap-2 bg-default px-3 py-3 border-b border-default">
        <UInput
          v-model="search"
          icon="i-lucide-search"
          :placeholder="t('uxLogSearch')"
          :aria-label="t('uxLogSearch')"
          class="w-full sm:w-72"
        />
        <USelect v-model="level" :items="levels" :aria-label="t('uxLogAllLevels')" class="w-36" />
        <USelect
          v-model="moduleFilter"
          :items="modules"
          :aria-label="t('uxLogAllModules')"
          class="w-44"
        />
        <div class="flex-1" />
        <div class="flex gap-2">
          <UButton
            :icon="live ? 'i-lucide-pause' : 'i-lucide-play'"
            color="neutral"
            variant="outline"
            @click="toggleLive"
            >{{ t(live ? 'uxLogPause' : 'uxLogResume') }}</UButton
          ><UButton
            icon="i-lucide-refresh-cw"
            variant="soft"
            :loading="busy"
            :disabled="busy"
            @click="refresh(true)"
            >{{ t('lgRefreshView') }}</UButton
          >
        </div>
        <span class="text-xs text-muted tabular-nums"
          >{{ filtered.length }} / {{ logs.length }}</span
        >
      </div>
      <div
        class="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 bg-slate-900 px-4 py-3 text-xs text-slate-400 border-b border-slate-700"
      >
        <div class="flex flex-wrap gap-x-4 gap-y-2">
          <span>{{ t(live ? 'lgFollowing' : 'lgPaused') }}</span
          ><span :class="logError ? 'text-rose-300' : 'text-slate-400'">{{
            logError
              ? t('lgFetchFailed')
              : lastFetched
                ? t('lgUpdated', { time: timestamp(lastFetched) })
                : t(busy ? 'uxLoading' : 'lgNotFetched')
          }}</span
          ><span>{{ t('uxLogBounded') }}</span>
        </div>
        <button
          v-if="!following"
          class="inline-flex items-center gap-1 text-indigo-300 hover:text-indigo-200"
          @click="jumpToLatest"
        >
          <UIcon name="i-lucide-arrow-down-to-line" class="size-3.5" />{{ t('uxLogFollow')
          }}<span v-if="newCount" class="ml-1 rounded bg-indigo-400/15 px-1.5 text-indigo-200">{{
            t('uxLogNewCount', { count: newCount })
          }}</span></button
        ><span v-else class="text-slate-400">{{ t('uxLogFollowing') }}</span>
      </div>
      <div
        ref="consolePanel"
        tabindex="0"
        :aria-label="t('uxLogConsole')"
        class="h-[min(62vh,640px)] min-h-72 overflow-auto bg-[#0b1220] text-slate-200 px-3 py-2 font-mono text-xs leading-[22px] focus-visible:outline-2 focus-visible:outline-indigo-400"
        @scroll.passive="onScroll"
      >
        <p v-if="!filtered.length" class="px-2 py-8 text-center text-slate-400">
          {{
            t(
              !lastFetched
                ? busy
                  ? 'uxLoading'
                  : 'lgNotFetched'
                : logs.length
                  ? 'uxLogNoMatches'
                  : 'emptyLogs',
            )
          }}
        </p>
        <div
          v-for="log in filtered"
          :key="log.id"
          class="border-b border-slate-800/40 last:border-0"
        >
          <button
            class="log-row w-full text-left items-start rounded-sm px-1 hover:bg-slate-800/50 focus-visible:outline-2 focus-visible:outline-indigo-400"
            :aria-expanded="expanded.has(log.id)"
            :aria-label="t('uxLogContext') + ': ' + log.code"
            @click="toggleContext(log.id)"
          >
            <UIcon
              :name="expanded.has(log.id) ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'"
              class="log-chevron mt-1.5 size-3 text-slate-500"
            />
            <time
              :datetime="log.at"
              class="log-time text-slate-500 tabular-nums whitespace-nowrap"
              >{{ timestamp(log.at) }}</time
            >
            <span
              class="log-level font-semibold uppercase"
              :class="
                log.level === 'error'
                  ? 'text-rose-400'
                  : log.level === 'warn'
                    ? 'text-amber-300'
                    : 'text-cyan-300'
              "
              >{{ log.level }}</span
            >
            <span class="log-module min-w-0 break-all text-violet-300">[{{ log.module }}]</span
            ><code class="log-code min-w-0 break-all">{{ log.code }}</code
            ><span
              v-if="log.durationMs !== undefined"
              class="log-duration text-right text-slate-500"
              >{{ log.durationMs }} ms</span
            >
          </button>
          <pre
            v-if="expanded.has(log.id)"
            class="ml-6 my-2 border-l border-slate-700 pl-3 whitespace-pre-wrap break-all text-slate-400 leading-5"
            >{{ JSON.stringify(context(log), null, 2) }}</pre>
        </div>
      </div>
    </section>
    <section class="rounded-lg border border-default bg-default p-5">
      <div class="flex flex-wrap items-center justify-between gap-3 mb-3">
        <h2 class="font-semibold">{{ t('visibleDiagnostics') }}</h2>
        <NuxtLink to="/settings#diagnostics" class="text-sm text-primary hover:underline">{{
          t('lgCaptureSettings')
        }}</NuxtLink>
      </div>
      <p class="text-muted text-sm mb-4">{{ t('visibleDiagnosticsHelp') }}</p>
      <p v-if="!diagnostics.length" class="text-sm text-muted">{{ t('lgNoDiagnostics') }}</p>
      <details
        v-for="entry in diagnostics"
        :key="entry.id"
        class="border-t border-default py-3 first:border-0"
      >
        <summary class="cursor-pointer text-sm">
          {{ roleLabel(entry.role) }} · {{ entry.modelId }} ·
          {{ new Date(entry.at).toLocaleString(locale) }}
        </summary>
        <h3 class="mt-3 font-medium text-sm">{{ t('modelInput') }}</h3>
        <pre
          class="text-xs whitespace-pre-wrap break-all bg-muted p-3 rounded-md max-h-80 overflow-auto"
          >{{ entry.input }}</pre>
        <h3 class="mt-3 font-medium text-sm">{{ t('modelOutput') }}</h3>
        <pre
          class="text-xs whitespace-pre-wrap break-all bg-muted p-3 rounded-md max-h-80 overflow-auto"
          >{{ entry.output }}</pre>
      </details>
    </section>
  </div>
</template>

<style scoped>
.log-row {
  display: grid;
  grid-template-columns: 12px 148px 44px 130px minmax(0, 1fr) 64px;
  gap: 8px;
  min-height: 24px;
}
.log-chevron {
  grid-column: 1;
}
.log-time {
  grid-column: 2;
}
.log-level {
  grid-column: 3;
}
.log-module {
  grid-column: 4;
}
.log-code {
  grid-column: 5;
}
.log-duration {
  grid-column: 6;
}
@media (max-width: 767px) {
  .log-row {
    grid-template-columns: 12px minmax(0, 1fr) 48px;
    gap: 2px 8px;
    padding-top: 6px;
    padding-bottom: 6px;
  }
  .log-chevron {
    grid-column: 1;
    grid-row: 1;
  }
  .log-time {
    grid-column: 2;
    grid-row: 1;
  }
  .log-level {
    grid-column: 3;
    grid-row: 1;
  }
  .log-module {
    grid-column: 2;
    grid-row: 2;
  }
  .log-duration {
    grid-column: 3;
    grid-row: 2;
  }
  .log-code {
    grid-column: 2 / 4;
    grid-row: 3;
  }
}
</style>
