<script setup lang="ts">
import { createModelCallPager, type CallFilters } from '../utils/model-call-pager'
const { t, locale } = useI18n()
const { client } = useApi()
const role = ref('*'),
  status = ref('*'),
  modelId = ref('')
const timezone = ref('')
const pager = createModelCallPager(async (query, signal) => {
  const result = await client.GET('/api/model-calls', { params: { query }, signal })
  if (!result.data) throw new Error('request failed')
  return result.data
})
const { state } = pager
const roles = ['planner', 'replyer', 'learning', 'vision', 'embedding']
const roleOptions = computed(() => [
  { value: '*', label: t('mcallsAllRoles') },
  ...roles.map((value) => ({ value, label: value })),
])
const statusOptions = computed(() => [
  { value: '*', label: t('mcallsAllStatuses') },
  ...['completed', 'failed'].map((value) => ({ value, label: t(`mcallsStatus_${value}`) })),
])
function apply() {
  const filters: CallFilters = {}
  if (modelId.value.trim()) filters.modelId = modelId.value.trim()
  if (role.value !== '*') filters.role = role.value as CallFilters['role']
  if (status.value === 'completed' || status.value === 'failed') filters.status = status.value
  void pager.reset(filters)
}
function reset() {
  modelId.value = ''
  role.value = '*'
  status.value = '*'
  void pager.reset({})
}
const number = (value: number | null, digits = 0) =>
  value === null
    ? t('mcallsUnknown')
    : value.toLocaleString(locale.value, { maximumFractionDigits: digits })
const timestamp = (at: string) =>
  new Date(at).toLocaleString(locale.value, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  })
onMounted(() => {
  timezone.value = Intl.DateTimeFormat().resolvedOptions().timeZone
  void pager.reset()
})
onBeforeUnmount(() => pager.dispose())
</script>

<template>
  <div class="space-y-4">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h1 class="page-heading">{{ t('mcallsTitle') }}</h1>
        <p class="mt-1 text-sm text-muted">{{ t('mcallsHelp') }}</p>
      </div>
      <UButton icon="i-lucide-refresh-cw" variant="soft" @click="pager.reset()">{{
        t('refresh')
      }}</UButton>
    </header>
    <form
      class="flex flex-wrap items-end gap-3 rounded-lg border border-default p-4"
      @submit.prevent="apply"
    >
      <UFormField :label="t('mcallsModel')"
        ><UInput
          v-model="modelId"
          :placeholder="t('mcallsModelPlaceholder')"
          class="w-full sm:w-64"
      /></UFormField>
      <UFormField :label="t('mcallsRole')"
        ><USelect v-model="role" :items="roleOptions" class="w-40"
      /></UFormField>
      <UFormField :label="t('mcallsStatus')"
        ><USelect v-model="status" :items="statusOptions" class="w-40"
      /></UFormField>
      <UButton type="submit">{{ t('mcallsApply') }}</UButton
      ><UButton color="neutral" variant="outline" @click="reset">{{ t('mcallsReset') }}</UButton>
    </form>
    <div class="text-sm text-muted space-y-1">
      <p>
        {{ t('mcallsRetention', { days: state.retentionDays, count: number(state.maxRecords) }) }}
      </p>
      <p>{{ t('mcallsMetricsHelp') }}</p>
      <p v-if="timezone">{{ t('mcallsTimezone', { timezone }) }}</p>
    </div>
    <UAlert v-if="state.failed" color="error" :title="t('mcallsFetchFailed')"
      ><template #actions
        ><UButton color="error" variant="soft" @click="pager.retry()">{{
          t('mcallsRetry')
        }}</UButton></template
      ></UAlert
    >
    <section
      class="rounded-lg border border-default overflow-hidden"
      :aria-label="t('mcallsTitle')"
      :aria-busy="state.busy"
    >
      <p v-if="state.busy" role="status" class="p-4 text-sm text-muted">{{ t('uxLoading') }}</p>
      <p
        v-else-if="state.loaded && !state.items.length"
        role="status"
        class="p-8 text-center text-muted"
      >
        {{ t('mcallsEmpty') }}
      </p>
      <div v-if="state.items.length" class="overflow-x-auto">
        <table class="w-full text-sm whitespace-nowrap">
          <caption class="sr-only">
            {{
              t('mcallsTitle')
            }}
          </caption>
          <thead class="bg-elevated text-muted text-left">
            <tr>
              <th
                v-for="key in [
                  'Time',
                  'Model',
                  'Role',
                  'Status',
                  'Transport',
                  'Attempt',
                  'Input',
                  'CacheRead',
                  'CacheWrite',
                  'Output',
                  'Duration',
                  'FirstToken',
                  'FirstTokenKind',
                  'Throughput',
                ]"
                :key="key"
                scope="col"
                class="px-4 py-3 font-medium"
              >
                {{ t('mcalls' + key) }}
              </th>
            </tr>
          </thead>
          <tbody class="divide-y divide-default">
            <tr v-for="call in state.items" :key="call.id" class="hover:bg-elevated/50">
              <td class="px-4 py-3 tabular-nums">
                <time :datetime="call.at" :title="call.at">{{ timestamp(call.at) }}</time
                ><span class="block text-xs text-muted">#{{ call.id }}</span>
              </td>
              <td class="px-4 py-3">
                <span class="block font-medium">{{ call.modelId }}</span
                ><span class="block text-xs text-muted"
                  >{{ call.providerId }} · {{ call.apiModelName }}</span
                >
              </td>
              <td class="px-4 py-3">{{ call.role }}</td>
              <td class="px-4 py-3">
                <UBadge
                  :color="call.status === 'completed' ? 'success' : 'error'"
                  variant="subtle"
                  >{{ t('mcallsStatus_' + call.status) }}</UBadge
                >
                <span v-if="call.failureReason" class="mt-1 block text-xs text-muted">{{
                  t('mcallsFailure_' + call.failureReason)
                }}</span>
              </td>
              <td class="px-4 py-3">
                <UBadge color="neutral" variant="subtle">{{
                  call.transportMode
                    ? t('mcallsTransport_' + call.transportMode)
                    : t('mcallsUnknown')
                }}</UBadge>
              </td>
              <td class="px-4 py-3 tabular-nums">{{ number(call.attempt) }}</td>
              <td
                v-for="key in [
                  'inputTokens',
                  'cacheReadInputTokens',
                  'cacheWriteInputTokens',
                  'outputTokens',
                ] as const"
                :key="key"
                class="px-4 py-3 tabular-nums"
              >
                {{ number(call[key]) }}
              </td>
              <td class="px-4 py-3 tabular-nums">{{ number(call.durationMs, 1) }} ms</td>
              <td class="px-4 py-3 tabular-nums">
                {{
                  call.firstTokenMs == null
                    ? t('mcallsUnknown')
                    : number(call.firstTokenMs, 1) + ' ms'
                }}
              </td>
              <td class="px-4 py-3">
                {{
                  call.firstTokenKind
                    ? t('mcallsFirstTokenKind_' + call.firstTokenKind)
                    : t('mcallsUnknown')
                }}
              </td>
              <td class="px-4 py-3 tabular-nums">{{ number(call.tokensPerSecond, 2) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <footer class="flex flex-wrap items-center justify-between gap-3 border-t border-default p-3">
        <span class="text-sm text-muted">{{
          t('mcallsPage', { page: state.page, count: state.items.length })
        }}</span>
        <div class="flex gap-2">
          <UButton
            color="neutral"
            variant="outline"
            :disabled="state.busy || state.page <= 1"
            @click="pager.previous()"
            >{{ t('mcallsPrevious') }}</UButton
          ><UButton
            color="neutral"
            variant="outline"
            :disabled="state.busy || state.nextCursor === null"
            @click="pager.next()"
            >{{ t('mcallsNext') }}</UButton
          >
        </div>
      </footer>
    </section>
    <NuxtLink
      to="/models"
      class="inline-flex items-center gap-1 text-sm text-primary hover:underline"
      >{{ t('mcallsModelsLink') }}<UIcon name="i-lucide-arrow-up-right" class="size-4"
    /></NuxtLink>
  </div>
</template>
