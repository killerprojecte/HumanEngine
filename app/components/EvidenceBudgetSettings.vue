<script setup lang="ts">
import type { components } from '~/types/api'
type Settings = components['schemas']['Settings']
const settings = defineModel<Settings>({ required: true })
const props = withDefaults(defineProps<{ disabled?: boolean }>(), { disabled: false })
const { t } = useI18n()
const { authenticated } = useApi()
const locked = computed(() => props.disabled || !authenticated.value)
</script>
<template>
  <section
    class="rounded-lg border border-default p-4 space-y-3"
    :aria-label="t('evidenceBudgetTitle')"
  >
    <div>
      <h2 class="font-semibold">{{ t('evidenceBudgetTitle') }}</h2>
      <p class="text-sm text-muted mt-1">{{ t('evidenceBudgetHelp') }}</p>
    </div>
    <div class="grid sm:grid-cols-2 gap-4">
      <label class="block space-y-1 text-sm">
        <span class="font-medium">{{ t('evidenceBudgetPlanner') }}</span>
        <input
          v-model.number="settings.plannerEvidenceMaxBytes"
          type="number"
          min="8192"
          max="2097152"
          step="1"
          :disabled="locked"
          class="block w-full rounded-md border border-default bg-default p-2"
        />
        <span class="block text-xs text-muted">{{ t('evidenceBudgetPlannerHelp') }}</span>
      </label>
      <label class="block space-y-1 text-sm">
        <span class="font-medium">{{ t('evidenceBudgetReplyer') }}</span>
        <input
          v-model.number="settings.replyEvidenceMaxBytes"
          type="number"
          min="4096"
          max="1048576"
          step="1"
          :disabled="locked"
          class="block w-full rounded-md border border-default bg-default p-2"
        />
        <span class="block text-xs text-muted">{{ t('evidenceBudgetReplyerHelp') }}</span>
      </label>
    </div>
    <p class="text-xs text-muted">{{ t('evidenceBudgetSelectionHelp') }}</p>
    <p class="text-xs text-muted">{{ t('evidenceBudgetSaveHelp') }}</p>
  </section>
</template>
