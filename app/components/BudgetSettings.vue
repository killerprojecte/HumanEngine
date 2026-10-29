<script setup lang="ts">
import { budgetPreset, type BudgetPresetName } from '../../src/budget/presets'
import type { components } from '~/types/api'
type Settings = components['schemas']['Settings']
const settings = defineModel<Settings>({ required: true })
const props = withDefaults(defineProps<{ disabled?: boolean }>(), { disabled: false })
const { t, locale } = useI18n()
const { authenticatedFetch, authenticated } = useApi()
type Coverage = {
  enabled: boolean
  mode: 'strict' | 'best_effort'
  windowStart: string
  knownUsd: number
  heldUsd: number
  remainingUsd: number
  carriedNanoUsd: number
  unresolvedNanoUsd: number
  partialAttempts: number
  unknownAttempts: number
  pendingAttempts: number
  unboundedAttempts: number
  overrunAttempts: number
  externalCosts: {
    knownTodayNanoUsd: number
    knownAllTimeNanoUsd: number
    unknownReceipts: number
    coverage: 'known_imported_only' | 'incomplete'
  }
  coverage: 'settled' | 'pending' | 'unbounded' | 'external_incomplete'
}
const coverage = ref<Coverage>(),
  busy = ref(false),
  error = ref(false)
const locked = computed(() => props.disabled || !authenticated.value)
const ratioInvalid = computed(
  () => settings.value.budgetRealtimeRatio + settings.value.budgetMaintenanceRatio > 0.5,
)
function preset(name: BudgetPresetName) {
  if (locked.value) return
  Object.assign(settings.value, budgetPreset(name))
}
const usdInvalid = computed(
  () =>
    settings.value.budgetPerTurnUsd > settings.value.budgetDailyUsd ||
    settings.value.budgetMaxUnresolvedUsd > settings.value.budgetDailyUsd,
)
let controller: AbortController | undefined,
  generation = 0,
  disposed = false
function clear() {
  ++generation
  controller?.abort()
  coverage.value = undefined
  busy.value = false
  error.value = false
}
async function refresh() {
  clear()
  if (disposed || !authenticated.value) return
  const current = generation
  controller = new AbortController()
  busy.value = true
  try {
    const response = await authenticatedFetch('/api/budget', { signal: controller.signal })
    if (!response.ok) throw new Error('budget_coverage_unavailable')
    const value = (await response.json()) as Coverage
    if (!disposed && current === generation && authenticated.value) coverage.value = value
  } catch {
    if (!disposed && current === generation && authenticated.value) error.value = true
  } finally {
    if (current === generation) busy.value = false
  }
}
const usd = (amount: number) =>
  new Intl.NumberFormat(locale.value, {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: 2,
    maximumFractionDigits: 9,
  }).format(amount)
const time = (value: string) =>
  Number.isFinite(Date.parse(value))
    ? new Date(value).toLocaleString(locale.value)
    : t('budgetUnknownTime')
watch(authenticated, () => (authenticated.value ? void refresh() : clear()))
onMounted(() => void refresh())
onBeforeUnmount(() => {
  disposed = true
  clear()
})
defineExpose({ refresh })
</script>
<template>
  <section class="rounded-lg border border-default p-4 space-y-4" :aria-label="t('budgetTitle')">
    <div>
      <h2 class="font-semibold">{{ t('budgetTitle') }}</h2>
      <p class="text-sm text-muted mt-1">{{ t('budgetHelp') }}</p>
    </div>
    <div class="flex gap-2 flex-wrap">
      <UButton :disabled="locked" variant="outline" @click="preset('strict_small')">{{
        t('budgetPresetStrict')
      }}</UButton>
      <UButton :disabled="locked" variant="outline" @click="preset('estimated_small')">{{
        t('budgetPresetEstimated')
      }}</UButton>
    </div>
    <p class="text-sm text-muted">{{ t('budgetPresetHelp') }}</p>
    <RecoveryModelHold />
    <ExternalCostSettings />
    <USwitch
      v-model="settings.budgetExperimentEnabled"
      :disabled="locked"
      :label="t('budgetEnabled')"
    />
    <label class="block space-y-1">
      <span class="text-sm font-medium">{{ t('budgetMode') }}</span>
      <select
        v-model="settings.budgetMode"
        :disabled="locked"
        class="block w-full rounded-md border border-default bg-default p-2"
      >
        <option value="strict">{{ t('budgetModeStrict') }}</option>
        <option value="best_effort">{{ t('budgetModeBestEffort') }}</option>
      </select>
    </label>
    <p class="text-sm text-warning" role="note">
      {{ t(settings.budgetMode === 'strict' ? 'budgetStrictWarning' : 'budgetBestEffortWarning') }}
    </p>
    <div class="grid sm:grid-cols-3 gap-3">
      <label class="space-y-1 text-sm">
        <span>{{ t('budgetDailyUsd') }}</span>
        <input
          v-model.number="settings.budgetDailyUsd"
          type="number"
          min="0.000001"
          max="100000"
          step="any"
          :disabled="locked"
          class="block w-full rounded-md border border-default bg-default p-2"
        />
      </label>
      <label class="space-y-1 text-sm">
        <span>{{ t('budgetPerTurnUsd') }}</span>
        <input
          v-model.number="settings.budgetPerTurnUsd"
          type="number"
          min="0.000001"
          max="100000"
          step="any"
          :disabled="locked"
          :aria-invalid="usdInvalid"
          class="block w-full rounded-md border border-default bg-default p-2"
        />
      </label>
      <label class="space-y-1 text-sm">
        <span>{{ t('budgetUnresolvedUsd') }}</span>
        <input
          v-model.number="settings.budgetMaxUnresolvedUsd"
          type="number"
          min="0.000001"
          max="100000"
          step="any"
          :disabled="locked"
          :aria-invalid="usdInvalid"
          class="block w-full rounded-md border border-default bg-default p-2"
        />
      </label>
    </div>
    <p v-if="usdInvalid" role="alert" class="text-sm text-error">{{ t('budgetUsdInvalid') }}</p>
    <details class="rounded-md border border-default p-3">
      <summary class="cursor-pointer font-medium text-sm">{{ t('budgetAdvanced') }}</summary>
      <div class="grid sm:grid-cols-3 gap-3 mt-3">
        <label class="space-y-1 text-sm">
          <span>{{ t('budgetTurnTokens') }}</span>
          <input
            v-model.number="settings.budgetPerTurnTokens"
            type="number"
            min="1"
            max="10000000"
            step="1"
            :disabled="locked"
            class="block w-full rounded-md border border-default bg-default p-2"
          />
        </label>
        <label class="space-y-1 text-sm">
          <span>{{ t('budgetTurnCalls') }}</span>
          <input
            v-model.number="settings.budgetPerTurnCalls"
            type="number"
            min="1"
            max="1000"
            step="1"
            :disabled="locked"
            class="block w-full rounded-md border border-default bg-default p-2"
          />
        </label>
        <label class="space-y-1 text-sm">
          <span>{{ t('budgetMaxOutput') }}</span>
          <input
            v-model.number="settings.budgetMaxOutputTokens"
            type="number"
            min="1"
            max="1000000"
            step="1"
            :disabled="locked"
            class="block w-full rounded-md border border-default bg-default p-2"
          />
        </label>
      </div>
      <div class="grid sm:grid-cols-2 gap-3 mt-3">
        <label class="space-y-1 text-sm">
          <span>{{ t('budgetRealtimeRatio') }}</span>
          <input
            v-model.number="settings.budgetRealtimeRatio"
            type="number"
            min="0"
            max="0.5"
            step="0.01"
            :disabled="locked"
            :aria-invalid="ratioInvalid"
            class="block w-full rounded-md border border-default bg-default p-2"
          />
        </label>
        <label class="space-y-1 text-sm">
          <span>{{ t('budgetMaintenanceRatio') }}</span>
          <input
            v-model.number="settings.budgetMaintenanceRatio"
            type="number"
            min="0"
            max="0.5"
            step="0.01"
            :disabled="locked"
            :aria-invalid="ratioInvalid"
            class="block w-full rounded-md border border-default bg-default p-2"
          />
        </label>
      </div>
      <p class="text-xs text-muted mt-2">{{ t('budgetRatioHelp') }}</p>
      <p v-if="ratioInvalid" role="alert" class="text-sm text-error mt-2">
        {{ t('budgetRatioInvalid') }}
      </p>
    </details>
    <p class="text-sm text-muted">{{ t('budgetSaveHelp') }}</p>
    <div class="border-t border-default pt-4 space-y-3">
      <div class="flex items-center justify-between gap-2">
        <h3 class="font-medium">{{ t('budgetCoverageTitle') }}</h3>
        <UButton variant="outline" size="sm" :disabled="!authenticated || busy" @click="refresh">{{
          t('refresh')
        }}</UButton>
      </div>
      <p class="text-xs text-muted">{{ t('budgetCoverageHelp') }}</p>
      <p v-if="!authenticated" class="text-sm text-muted">{{ t('budgetSignIn') }}</p>
      <p v-else-if="busy" role="status" class="text-sm">{{ t('uxLoading') }}</p>
      <p v-else-if="error" role="alert" class="text-sm text-error">{{ t('budgetLoadError') }}</p>
      <template v-else-if="coverage">
        <p class="text-sm">
          {{ t(coverage.enabled ? 'budgetSavedEnabled' : 'budgetSavedDisabled') }} ·
          {{ t(coverage.mode === 'strict' ? 'budgetModeStrict' : 'budgetModeBestEffort') }}
        </p>
        <p class="text-xs text-muted">
          {{ t('budgetWindow', { time: time(coverage.windowStart) }) }}
        </p>
        <p :class="coverage.coverage === 'settled' ? 'text-muted' : 'text-warning'" class="text-sm">
          {{ t('budgetCoverage_' + coverage.coverage) }}
        </p>
        <p class="text-xs text-muted">
          {{
            t('ecCoverage', {
              today: usd(coverage.externalCosts.knownTodayNanoUsd / 1e9),
              total: usd(coverage.externalCosts.knownAllTimeNanoUsd / 1e9),
              unknown: coverage.externalCosts.unknownReceipts,
            })
          }}
        </p>
        <dl class="grid sm:grid-cols-3 gap-3 text-sm">
          <div>
            <dt class="text-muted">{{ t('budgetKnownUsd') }}</dt>
            <dd>{{ usd(coverage.knownUsd) }}</dd>
          </div>
          <div>
            <dt class="text-muted">{{ t('budgetHeldUsd') }}</dt>
            <dd>{{ usd(coverage.heldUsd) }}</dd>
          </div>
          <div>
            <dt class="text-muted">{{ t('budgetCarriedUsd') }}</dt>
            <dd>{{ usd(coverage.carriedNanoUsd / 1e9) }}</dd>
          </div>
          <div>
            <dt class="text-muted">{{ t('budgetRemainingUsd') }}</dt>
            <dd>{{ usd(coverage.remainingUsd) }}</dd>
          </div>
        </dl>
        <p class="text-xs text-muted">
          {{ t('budgetUnresolvedTotal', { amount: usd(coverage.unresolvedNanoUsd / 1e9) }) }}
        </p>
        <p class="text-xs text-muted">
          {{
            t('budgetAttemptCounts', {
              partial: coverage.partialAttempts,
              unknown: coverage.unknownAttempts,
              pending: coverage.pendingAttempts,
              unbounded: coverage.unboundedAttempts,
              overrun: coverage.overrunAttempts,
            })
          }}
        </p>
        <p class="text-xs text-muted">{{ t('budgetNotInvoice') }}</p>
      </template>
    </div>
  </section>
</template>
