<script setup lang="ts">
import type { components } from '~/types/api'
const settings = defineModel<components['schemas']['Settings']>({ required: true })
const props = withDefaults(defineProps<{ disabled?: boolean }>(), { disabled: false })
const { authenticated } = useApi()
const { t } = useI18n()
const locked = computed(() => props.disabled || !authenticated.value)
</script>
<template>
  <section class="rounded-lg border border-default p-4 space-y-3">
    <h2 class="font-semibold">{{ t('exSettingsTitle') }}</h2>
    <p class="text-sm text-muted">{{ t('exSettingsHelp') }}</p>
    <USwitch v-model="settings.memoryExperienceEnabled" :disabled="locked" :label="t('exEnable')" />
    <p class="text-sm text-muted">{{ t('exEnableHelp') }}</p>
    <USwitch
      v-model="settings.memoryExperienceAutoActivateEnabled"
      :disabled="locked"
      :label="t('exAuto')"
    />
    <p class="text-sm text-muted">{{ t('exAutoHelp') }}</p>
    <USwitch
      v-model="settings.memoryExperienceHintsEnabled"
      :disabled="locked"
      :label="t('exHints')"
    />
    <p class="text-sm text-muted">{{ t('exHintsHelp') }}</p>
    <USwitch
      v-model="settings.memoryExperienceFeedbackEnabled"
      :disabled="locked"
      :label="t('exFeedbackTrials')"
    />
    <p class="text-sm text-muted">{{ t('exFeedbackTrialsHelp') }}</p>
  </section>
</template>
