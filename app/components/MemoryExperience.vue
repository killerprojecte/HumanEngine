<script setup lang="ts">
import type { components } from '~/types/api'
type Policy = {
  id: string
  version: number
  action: string
  condition: { kind: string; personId?: string }
  personName?: string
  status: string
  activationBasis?: string
  trialEligible: boolean
  trial?: {
    version: number
    policyVersion: number
    evidencePolicyVersion: number
    phase: string
    requesterName?: string
    createdAt: string
    expiresAt: string
    positiveCount: number
    instruction: { id: string; version: number }
    reason?: string
  }
  createdAt: string
  expiresAt: string
  expired: boolean
  sources: Array<{ messageId: string; senderName?: string; observedAt: string; code: string }>
  validation?: { tier: string; checks: string[]; failures: string[]; passed: boolean }
  uses: { exposed: number; applied: number; declined: number }
}
const props = defineProps<{ scenes: components['schemas']['Scene'][] }>()
const { authenticated, authenticatedFetch } = useApi()
const { t, locale } = useI18n()
const sceneId = ref(''),
  status = ref(''),
  items = ref<Policy[]>([]),
  enabled = ref(false),
  limited = ref(false),
  busy = ref(false),
  error = ref('')
const scenes = computed(() =>
  props.scenes.filter((scene) => ['group', 'private'].includes(scene.kind)),
)
let epoch = 0,
  disposed = false,
  controller: AbortController | undefined
const date = (value: string) =>
  Number.isFinite(Date.parse(value))
    ? new Date(value).toLocaleString(locale.value)
    : t('mpUnknownTime')
async function load() {
  const current = ++epoch
  controller?.abort()
  controller = new AbortController()
  items.value = []
  limited.value = false
  enabled.value = false
  error.value = ''
  busy.value = false
  if (disposed || !authenticated.value || !sceneId.value) return
  busy.value = true
  try {
    const response = await authenticatedFetch(
      `/api/memory-experience?${new URLSearchParams({ sceneId: sceneId.value, ...(status.value ? { status: status.value } : {}) })}`,
      { signal: controller.signal },
    )
    if (!response.ok) throw new Error('request')
    const result = await response.json()
    if (disposed || current !== epoch || !authenticated.value) return
    items.value = result.items
    enabled.value = result.enabled
    limited.value = result.coverage === 'limited'
  } catch {
    if (current === epoch && authenticated.value) error.value = t('networkError')
  } finally {
    if (current === epoch) busy.value = false
  }
}
async function act(policy: Policy, action: string) {
  if (busy.value || !authenticated.value || !window.confirm(t('exConfirm_' + action))) return
  const current = epoch
  controller?.abort()
  controller = new AbortController()
  busy.value = true
  error.value = ''
  try {
    const base = `/api/memory-experience/${encodeURIComponent(policy.id)}`
    const response = await authenticatedFetch(
      action === 'delete' ? base : base + (action === 'validate' ? '/validate' : '/review'),
      {
        method: action === 'delete' ? 'DELETE' : 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          sceneId: sceneId.value,
          expectedVersion: policy.version,
          ...(['active', 'suspended', 'retired'].includes(action) ? { status: action } : {}),
        }),
        signal: controller.signal,
      },
    )
    if (!response.ok) throw new Error(response.status === 409 ? 'conflict' : 'request')
    if (current === epoch && authenticated.value) await load()
  } catch (cause) {
    if (current === epoch && authenticated.value)
      error.value = t(
        cause instanceof Error && cause.message === 'conflict' ? 'exConflict' : 'exUncertain',
      )
  } finally {
    if (current === epoch) busy.value = false
  }
}
watch([sceneId, status, authenticated], () => void load())
watch(scenes, (rows) => {
  if (sceneId.value && !rows.some((row) => row.id === sceneId.value)) sceneId.value = ''
})
onBeforeUnmount(() => {
  disposed = true
  epoch++
  controller?.abort()
})
</script>
<template>
  <section class="rounded-lg border border-default p-4 space-y-3">
    <h2 class="font-semibold">{{ t('exTitle') }}</h2>
    <p class="text-sm text-muted">{{ t('exHelp') }}</p>
    <div class="flex flex-wrap gap-2">
      <select
        v-model="sceneId"
        :disabled="!authenticated"
        :aria-label="t('mpScene')"
        class="rounded border border-default bg-default p-2"
      >
        <option value="">{{ t('mpChooseScene') }}</option>
        <option v-for="scene in scenes" :key="scene.id" :value="scene.id">
          {{ scene.name || t('mpUnnamedScene') }}
        </option>
      </select>
      <select
        v-model="status"
        :disabled="!authenticated"
        :aria-label="t('exStatusFilter')"
        class="rounded border border-default bg-default p-2"
      >
        <option value="">{{ t('exAll') }}</option>
        <option
          v-for="state in ['candidate', 'validated', 'active', 'suspended', 'retired']"
          :key="state"
          :value="state"
        >
          {{ t('exStatus_' + state) }}
        </option>
      </select>
      <UButton :disabled="busy || !sceneId || !authenticated" variant="outline" @click="load">{{
        t('refresh')
      }}</UButton>
    </div>
    <p v-if="!authenticated">{{ t('mpSignIn') }}</p>
    <p v-if="busy" role="status">{{ t('uxLoading') }}</p>
    <p v-if="error" role="alert" class="text-error">{{ error }}</p>
    <p v-if="sceneId && !busy && !error && !enabled" class="text-sm text-muted">
      {{ t('exDisabled') }}
    </p>
    <p v-else-if="sceneId && !busy && !error && !items.length" class="text-sm text-muted">
      {{ t('exEmpty') }}
    </p>
    <p v-if="limited" class="text-sm text-muted">{{ t('exLimited') }}</p>
    <article
      v-for="policy in items"
      :key="policy.id"
      class="border border-default rounded p-3 space-y-2"
    >
      <div class="text-xs text-muted flex gap-2">
        <span>{{ t('exStatus_' + policy.status) }}</span
        ><span v-if="policy.expired">{{ t('exExpired') }}</span
        ><span v-if="policy.activationBasis">{{ t('exBasis_' + policy.activationBasis) }}</span>
      </div>
      <h3 class="font-medium">{{ t('exAction_' + policy.action) }}</h3>
      <p class="text-sm">
        {{ t('exCondition_' + policy.condition.kind)
        }}<span v-if="policy.condition.personId">
          · {{ policy.personName || t('mpUnknownPerson') }}</span
        >
      </p>
      <dl class="grid sm:grid-cols-2 gap-2 text-xs">
        <div>
          <dt>{{ t('exCreated') }}</dt>
          <dd>{{ date(policy.createdAt) }}</dd>
        </div>
        <div>
          <dt>{{ t('exExpires') }}</dt>
          <dd>{{ date(policy.expiresAt) }}</dd>
        </div>
      </dl>
      <section v-if="policy.trial" class="space-y-2 rounded border border-default p-3">
        <h4 class="font-medium">
          {{ t('exTrialTitle') }} · {{ t('exTrialPhase_' + policy.trial.phase) }}
        </h4>
        <p class="text-sm text-muted">{{ t('exTrialHelp') }}</p>
        <p>{{ t(policy.trialEligible ? 'exTrialEligible' : 'exTrialIneligible') }}</p>
        <p>{{ t('exTrialRequester') }}: {{ policy.trial.requesterName || t('mpUnknownPerson') }}</p>
        <p>
          {{
            t('exTrialVersions', {
              receipt: policy.trial.version,
              policy: policy.trial.policyVersion,
              evidence: policy.trial.evidencePolicyVersion,
            })
          }}
        </p>
        <p>
          {{ t('exCreated') }}: {{ date(policy.trial.createdAt) }} · {{ t('exExpires') }}:
          {{ date(policy.trial.expiresAt) }}
        </p>
        <p>{{ t('exTrialSupport', { count: policy.trial.positiveCount }) }}</p>
        <p v-if="policy.trial.reason">{{ t('exTrialReason_' + policy.trial.reason) }}</p>
        <details>
          <summary>{{ t('exTrialInstruction') }}</summary>
          <p class="break-all">
            {{ policy.trial.instruction.id }} / v{{ policy.trial.instruction.version }}
          </p>
        </details>
      </section>
      <p class="text-sm">{{ t('exCounts', policy.uses) }}</p>
      <p class="text-xs text-muted">{{ t('exCountsHelp') }}</p>
      <details class="text-sm">
        <summary>{{ t('exEvidence') }}</summary>
        <p class="text-xs text-muted">{{ t('exMechanicalHelp') }}</p>
        <p v-if="!policy.validation">{{ t('exNotValidated') }}</p>
        <template v-else
          ><p>
            {{ t('exMechanical') }} · {{ t(policy.validation.passed ? 'exPassed' : 'exFailed') }}
          </p>
          <ul class="list-disc pl-5">
            <li v-for="check in policy.validation.checks" :key="check">
              {{ t('exCheck_' + check) }}
            </li>
          </ul>
          <p v-if="policy.validation.failures.length">
            {{ t('exFailed') }}: {{ policy.validation.failures.length }}
          </p></template
        >
        <div v-for="source in policy.sources" :key="source.messageId" class="mt-2 text-xs">
          <p>
            {{ t('exFeedback_' + source.code) }} · {{ source.senderName || t('mpUnknownPerson') }}
          </p>
          <p>{{ date(source.observedAt) }}</p>
          <p class="break-all">{{ t('mpSourceMessage') }}: {{ source.messageId }}</p>
        </div>
      </details>
      <div class="flex flex-wrap gap-2">
        <UButton
          v-if="['candidate', 'suspended'].includes(policy.status) && !policy.expired"
          :disabled="busy || !authenticated"
          variant="outline"
          @click="act(policy, 'validate')"
          >{{ t('exValidate') }}</UButton
        ><UButton
          v-if="policy.status === 'validated' && !policy.expired"
          :disabled="busy || !authenticated"
          @click="act(policy, 'active')"
          >{{ t('exActivate') }}</UButton
        ><UButton
          v-if="['active', 'validated', 'candidate'].includes(policy.status)"
          :disabled="busy || !authenticated"
          variant="outline"
          @click="act(policy, 'suspended')"
          >{{ t('exSuspend') }}</UButton
        ><UButton
          v-if="policy.status !== 'retired'"
          :disabled="busy || !authenticated"
          variant="outline"
          @click="act(policy, 'retired')"
          >{{ t('exRetire') }}</UButton
        ><UButton
          color="error"
          variant="outline"
          :disabled="busy || !authenticated"
          @click="act(policy, 'delete')"
          >{{ t('exDelete') }}</UButton
        >
      </div>
    </article>
  </section>
</template>
