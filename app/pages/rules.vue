<script setup lang="ts">
import type { components } from '~/types/api'
type Rule = components['schemas']['Rule']
type Habit = components['schemas']['Habit']
type Scene = components['schemas']['Scene']
type Version = { id: string; createdAt: string; content: string }
const { t, locale } = useI18n(),
  { client } = useApi()
const rules = ref<Rule[]>([]),
  habits = ref<Habit[]>([]),
  scenes = ref<Scene[]>([]),
  versions = ref<Version[]>([])
const currentPersona = ref('')
const tab = ref('persona'),
  query = ref(''),
  scopeFilter = ref('*')
const busy = ref(false),
  loaded = ref(false),
  error = ref(''),
  success = ref('')
const editOpen = ref(false),
  hintOpen = ref(false),
  draft = ref<Rule>(),
  selectedHabit = ref<Habit>()
const isNew = ref(false),
  recoveryRequired = ref(false),
  editorUncertain = ref(false)
let original: Rule | undefined,
  openingSnapshot = ''
const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value))
const dirty = computed(() => editOpen.value && JSON.stringify(draft.value) !== openingSnapshot)
const blocked = computed(() => busy.value || recoveryRequired.value || editorUncertain.value)
const valid = computed(
  () => !!draft.value?.content.trim() && (draft.value.scope === 'global' || !!draft.value.sceneId),
)
const sceneName = (id?: string) => scenes.value.find((scene) => scene.id === id)?.name ?? id ?? ''
const scopeName = (rule: Rule) =>
  rule.scope === 'global' ? t('rulesGlobal') : sceneName(rule.sceneId)
const habitName = (habit: Habit) =>
  habit.kind === 'sceneRule' ? (habit.content ?? '') : t(`ruPreference_${habit.preference}`)
const habitScope = (habit: Habit) =>
  habit.kind === 'botExpression' ? t('ruBotExpression') : sceneName(habit.sceneId)
const matches = (...values: string[]) =>
  values.join(' ').toLocaleLowerCase().includes(query.value.trim().toLocaleLowerCase())
const visibleRules = computed(() =>
  rules.value.filter(
    (rule) =>
      (scopeFilter.value === '*' ||
        (scopeFilter.value === 'global'
          ? rule.scope === 'global'
          : rule.scope === 'scene' && rule.sceneId === scopeFilter.value.slice(6))) &&
      matches(rule.content, scopeName(rule)),
  ),
)
const visibleHabits = computed(() =>
  habits.value.filter(
    (habit) =>
      (scopeFilter.value === '*' ||
        (scopeFilter.value === 'global'
          ? habit.kind === 'botExpression'
          : habit.kind === 'sceneRule' && habit.sceneId === scopeFilter.value.slice(6))) &&
      matches(habitName(habit), habitScope(habit)),
  ),
)
const confirmation = ref<'discard' | 'ruleDelete' | 'habitDelete' | 'habitLock'>()
const confirmationKind = ref<'discard' | 'ruleDelete' | 'habitDelete' | 'habitLock'>('discard')
const confirmationCopy = ref({ title: '', help: '', preview: '', action: '' })
const confirmationError = ref('')
let confirmedRule: Rule | undefined,
  confirmedHabit: Habit | undefined,
  pendingNavigation: ((allow: boolean) => void) | undefined
class KnownFailure extends Error {}
function requireSuccess(response: Response, message: string): never {
  if (response.status >= 400 && response.status < 500) throw new KnownFailure(message)
  throw new Error(message)
}
function failed(cause: unknown, mutationStarted: boolean) {
  if (mutationStarted && !(cause instanceof KnownFailure)) {
    recoveryRequired.value = true
    editorUncertain.value = true
  }
  return cause instanceof Error ? cause.message : t('networkError')
}
async function load(reconcile = false) {
  if (busy.value || (!reconcile && (editOpen.value || hintOpen.value))) return
  busy.value = true
  error.value = ''
  success.value = ''
  try {
    const [r, h, s, v, settings] = await Promise.all([
      client.GET('/api/rules'),
      client.GET('/api/habits'),
      client.GET('/api/scenes'),
      client.GET('/api/persona/versions'),
      client.GET('/api/settings'),
    ])
    if (!r.data || !h.data || !s.data || !v.data || !settings.data)
      throw new Error(
        r.error?.error ??
          h.error?.error ??
          s.error?.error ??
          v.error?.error ??
          settings.error?.error ??
          t('networkError'),
      )
    rules.value = r.data
    habits.value = h.data
    scenes.value = s.data
    versions.value = v.data
    currentPersona.value = settings.data.persona
    recoveryRequired.value = false
    loaded.value = true
  } catch (cause) {
    error.value = failed(cause, false)
    if (confirmation.value) confirmationError.value = error.value
  } finally {
    busy.value = false
  }
}
function begin(rule?: Rule) {
  if (busy.value || recoveryRequired.value) return
  original = rule ? clone(rule) : undefined
  draft.value = rule
    ? clone(rule)
    : {
        id: crypto.randomUUID(),
        scope: 'global',
        content: '',
        enabled: true,
        nonOverridable: false,
      }
  isNew.value = !rule
  openingSnapshot = JSON.stringify(draft.value)
  editorUncertain.value = false
  error.value = ''
  success.value = ''
  editOpen.value = true
}
function openHint(habit: Habit) {
  if (busy.value || recoveryRequired.value) return
  selectedHabit.value = clone(habit)
  editorUncertain.value = false
  error.value = ''
  success.value = ''
  hintOpen.value = true
}
function ask(kind: NonNullable<typeof confirmation.value>) {
  if (busy.value || (kind !== 'discard' && blocked.value)) return
  confirmedRule = original ? clone(original) : undefined
  confirmedHabit = selectedHabit.value ? clone(selectedHabit.value) : undefined
  const removing = kind === 'ruleDelete' || kind === 'habitDelete'
  confirmationCopy.value = {
    title: t(
      kind === 'discard'
        ? 'mcDiscardTitle'
        : removing
          ? 'ruDeleteTitle'
          : confirmedHabit?.locked
            ? 'ruUnlockTitle'
            : 'ruLockTitle',
    ),
    help: t(
      kind === 'discard'
        ? 'ruDiscardHelp'
        : kind === 'ruleDelete'
          ? 'ruDeleteRuleHelp'
          : kind === 'habitDelete'
            ? 'ruDeleteHintHelp'
            : 'ruLockHelp',
    ),
    preview:
      kind === 'ruleDelete' && confirmedRule
        ? `${scopeName(confirmedRule)} · ${confirmedRule.content}`
        : kind !== 'discard' && confirmedHabit
          ? `${habitScope(confirmedHabit)} · ${habitName(confirmedHabit)}`
          : '',
    action: t(
      kind === 'discard'
        ? 'mcDiscard'
        : removing
          ? 'confirmDelete'
          : confirmedHabit?.locked
            ? 'unlock'
            : 'lock',
    ),
  }
  confirmationError.value = ''
  confirmationKind.value = kind
  confirmation.value = kind
}
function closeEdit() {
  editOpen.value = false
  confirmation.value = undefined
  error.value = ''
}
function requestClose(open = false) {
  if (open || busy.value) return
  if (dirty.value) ask('discard')
  else closeEdit()
}
function cancelConfirmation() {
  if (busy.value) return
  confirmation.value = undefined
  pendingNavigation?.(false)
  pendingNavigation = undefined
}
function discard() {
  closeEdit()
  pendingNavigation?.(true)
  pendingNavigation = undefined
}
function changeScope(value: string) {
  if (!draft.value) return
  draft.value.scope = value === 'scene' ? 'scene' : 'global'
  if (draft.value.scope === 'global') delete draft.value.sceneId
  else draft.value.sceneId = scenes.value[0]?.id ?? ''
}
async function persistRule(deleting = false) {
  if (blocked.value || !draft.value || (!deleting && !valid.value)) return
  const target = deleting ? confirmedRule : original
  const proposed = clone(draft.value)
  if (deleting && !target) return
  let mutationStarted = false
  busy.value = true
  error.value = ''
  confirmationError.value = ''
  try {
    const latest = await client.GET('/api/rules')
    if (!latest.data) throw new Error(latest.error?.error ?? t('networkError'))
    const next = clone(latest.data)
    const id = target?.id ?? proposed.id
    const matches = next.filter((rule) => rule.id === id)
    if (
      target
        ? matches.length !== 1 || JSON.stringify(matches[0]) !== JSON.stringify(target)
        : matches.length !== 0
    )
      throw new Error(t('mcConfigChanged'))
    const index = next.findIndex((rule) => rule.id === id)
    if (deleting) next.splice(index, 1)
    else {
      proposed.content = proposed.content.trim()
      if (proposed.scope === 'global') delete proposed.sceneId
      if (index < 0) next.push(proposed)
      else next[index] = proposed
    }
    mutationStarted = true
    const result = await client.PUT('/api/rules', { body: next })
    if (!result.data) requireSuccess(result.response, result.error?.error ?? t('networkError'))
    rules.value = result.data
    closeEdit()
    success.value = t(deleting ? 'ruRuleDeleted' : 'ruRuleSaved')
  } catch (cause) {
    error.value = failed(cause, mutationStarted)
    if (confirmation.value) confirmationError.value = error.value
  } finally {
    busy.value = false
  }
}
async function confirm() {
  if (busy.value || !confirmation.value) return
  if (confirmation.value === 'discard') {
    discard()
    return
  }
  if (blocked.value) return
  if (confirmation.value === 'ruleDelete') {
    await persistRule(true)
    return
  }
  if (!confirmedHabit) return
  const target = clone(confirmedHabit),
    deleting = confirmation.value === 'habitDelete'
  let mutationStarted = false
  busy.value = true
  confirmationError.value = ''
  try {
    const latest = await client.GET('/api/habits')
    if (!latest.data) throw new Error(latest.error?.error ?? t('networkError'))
    const current = latest.data.find((habit) => habit.id === target.id)
    if (!current || JSON.stringify(current) !== JSON.stringify(target))
      throw new Error(t('mcConfigChanged'))
    mutationStarted = true
    if (deleting) {
      const result = await client.DELETE('/api/habits/{id}', {
        params: { path: { id: target.id } },
      })
      if (!result.data?.ok)
        requireSuccess(result.response, result.error?.error ?? t('networkError'))
      habits.value = latest.data.filter((habit) => habit.id !== target.id)
      hintOpen.value = false
    } else {
      const result = await client.PATCH('/api/habits/{id}', {
        params: { path: { id: target.id } },
        body: { locked: !target.locked },
      })
      if (!result.data) requireSuccess(result.response, result.error?.error ?? t('networkError'))
      habits.value = latest.data.map((habit) => (habit.id === target.id ? result.data! : habit))
      selectedHabit.value = clone(result.data)
    }
    confirmation.value = undefined
    success.value = t('ruHintUpdated')
  } catch (cause) {
    confirmationError.value = failed(cause, mutationStarted)
  } finally {
    busy.value = false
  }
}
watch(tab, () => {
  query.value = ''
  scopeFilter.value = '*'
})
onBeforeRouteLeave(() => {
  if (busy.value) return false
  if (!dirty.value) return true
  ask('discard')
  return new Promise<boolean>((resolve) => {
    pendingNavigation?.(false)
    pendingNavigation = resolve
  })
})
function beforeUnload(event: BeforeUnloadEvent) {
  if (dirty.value) {
    event.preventDefault()
    event.returnValue = ''
  }
}
onMounted(() => {
  void load()
  window.addEventListener('beforeunload', beforeUnload)
})
onBeforeUnmount(() => {
  pendingNavigation?.(false)
  window.removeEventListener('beforeunload', beforeUnload)
})
const date = (at: string) => new Date(at).toLocaleString(locale.value)
</script>
<template>
  <div class="space-y-5">
    <header class="flex justify-between items-start gap-4">
      <div>
        <h1 class="page-heading">{{ t('ruTitle') }}</h1>
        <p class="mt-2 text-sm text-muted">{{ t('ruIntro') }}</p>
      </div>
      <UButton
        class="shrink-0 whitespace-nowrap"
        icon="i-lucide-refresh-cw"
        variant="outline"
        color="neutral"
        :loading="busy && !editOpen && !hintOpen"
        :disabled="busy || editOpen || hintOpen"
        @click="load()"
        >{{ t('refresh') }}</UButton
      >
    </header>
    <UAlert v-if="error && !editOpen && !hintOpen" color="error" :title="error" /><UAlert
      v-if="success"
      color="success"
      variant="subtle"
      :title="success"
    />
    <div v-if="recoveryRequired && !editOpen && !hintOpen" class="space-y-3">
      <UAlert
        color="warning"
        :title="t('mrUnknownTitle')"
        :description="t('ruRecoveryHelp')"
      /><UButton :disabled="busy" @click="load(true)">{{ t('mrReload') }}</UButton>
    </div>
    <section class="surface overflow-hidden">
      <nav
        class="flex gap-5 px-4 overflow-x-auto border-b border-default"
        :aria-label="t('ruTitle')"
      >
        <button
          v-for="item in [
            { id: 'persona', label: t('ruCurrentPersona') },
            { id: 'manual', label: t('manualRules') },
            { id: 'learned', label: t('ruLearned') },
            { id: 'history', label: t('personaVersions') },
          ]"
          :key="item.id"
          class="h-12 shrink-0 border-b-2 text-sm font-medium"
          :class="tab === item.id ? 'border-primary text-primary' : 'border-transparent text-muted'"
          :aria-current="tab === item.id ? 'page' : undefined"
          @click="tab = item.id"
        >
          {{ item.label }}
        </button>
      </nav>
      <div
        v-if="tab === 'manual' || tab === 'learned'"
        class="flex flex-wrap items-center gap-3 p-4 border-b border-default"
      >
        <UInput
          v-model="query"
          icon="i-lucide-search"
          :placeholder="t('ruSearch')"
          :aria-label="t('ruSearch')"
          class="w-full sm:w-72"
        /><USelect
          v-model="scopeFilter"
          :aria-label="t('memoryScope')"
          :items="[
            { label: t('mmAllScopes'), value: '*' },
            { label: tab === 'manual' ? t('rulesGlobal') : t('ruBotExpression'), value: 'global' },
            ...scenes.map((scene) => ({ label: scene.name, value: 'scene:' + scene.id })),
          ]"
          class="w-full sm:w-52"
        />
        <div class="flex-1" />
        <UButton
          v-if="tab === 'manual'"
          icon="i-lucide-plus"
          :disabled="busy || !loaded || recoveryRequired"
          @click="begin()"
          >{{ t('ruAddRule') }}</UButton
        >
      </div>
      <p v-if="!loaded && busy" class="p-10 text-center text-muted">{{ t('uxLoading') }}</p>
      <div v-else-if="tab === 'persona'" class="p-4 sm:p-5 space-y-4">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <p class="text-sm text-muted leading-6 max-w-2xl">{{ t('ruPersonaHelp') }}</p>
          <UButton to="/settings#identity" variant="outline" color="neutral">{{
            t('editPersona')
          }}</UButton>
        </div>
        <p class="whitespace-pre-wrap break-words text-sm leading-7">
          {{ currentPersona || t('ruPersonaUnavailable') }}
        </p>
      </div>
      <template v-else-if="tab === 'manual'">
        <p class="px-4 py-3 text-xs leading-5 text-muted border-b border-default">
          {{ t('rulesHierarchy') }}
        </p>
        <article
          v-for="rule in visibleRules"
          :key="rule.id"
          class="p-4 flex items-start gap-4 border-b border-default last:border-0"
        >
          <div class="min-w-0 flex-1">
            <div class="flex flex-wrap gap-x-3 gap-y-1 items-center text-xs text-muted">
              <span class="break-words">{{ scopeName(rule) }}</span
              ><UBadge variant="subtle" color="neutral">{{
                t(rule.enabled ? 'enabled' : 'disabled')
              }}</UBadge
              ><span v-if="rule.nonOverridable">{{ t('rulesNonOverridable') }}</span>
            </div>
            <p class="mt-2 text-sm leading-6 whitespace-pre-line break-words line-clamp-3">
              {{ rule.content }}
            </p>
          </div>
          <UButton
            class="shrink-0"
            variant="ghost"
            color="neutral"
            :disabled="busy || recoveryRequired"
            @click="begin(rule)"
            >{{ t('edit') }}</UButton
          >
        </article>
        <p v-if="loaded && !visibleRules.length" class="p-10 text-center text-muted">
          {{ t(rules.length ? 'uxNoMatches' : 'rulesEmpty') }}
        </p>
      </template>
      <template v-else-if="tab === 'learned'">
        <p class="px-4 py-3 text-sm leading-6 text-muted border-b border-default">
          {{ t('ruLearnedHelp') }}
        </p>
        <article
          v-for="habit in visibleHabits"
          :key="habit.id"
          class="p-4 flex items-start gap-4 border-b border-default last:border-0"
        >
          <div class="flex-1 min-w-0">
            <p class="text-xs text-muted break-words">
              {{ habitScope(habit) }} · {{ t('ruStatus_' + habit.status)
              }}<span v-if="habit.locked"> · {{ t('mmLocked') }}</span>
            </p>
            <p class="mt-2 text-sm leading-6 break-words line-clamp-3">{{ habitName(habit) }}</p>
            <p class="mt-2 text-xs text-muted">
              {{ t('weight') }} {{ habit.weight.toFixed(2) }} · {{ t('confidence') }}
              {{ Math.round(habit.confidence * 100) }}%
            </p>
          </div>
          <UButton
            class="shrink-0"
            color="neutral"
            variant="ghost"
            :disabled="busy || recoveryRequired"
            @click="openHint(habit)"
            >{{ t('scDetails') }}</UButton
          >
        </article>
        <p v-if="loaded && !visibleHabits.length" class="p-10 text-center text-muted">
          {{ t(habits.length ? 'uxNoMatches' : 'ruNoHints') }}
        </p>
      </template>
      <div v-else class="p-4 sm:p-5 space-y-4">
        <div class="flex flex-wrap justify-between gap-3">
          <p class="text-sm leading-6 text-muted max-w-2xl">{{ t('ruHistoryHelp') }}</p>
          <UButton to="/settings#identity" color="neutral" variant="outline">{{
            t('editPersona')
          }}</UButton>
        </div>
        <p v-if="loaded && !versions.length" class="py-6 text-sm text-muted">
          {{ t('personaVersionsEmpty') }}
        </p>
        <details
          v-for="version in [...versions].reverse()"
          :key="version.id"
          class="border-t border-default pt-4"
        >
          <summary class="cursor-pointer text-sm font-medium">
            {{ date(version.createdAt) }}
          </summary>
          <p class="mt-3 whitespace-pre-wrap break-words text-sm leading-7">
            {{ version.content }}
          </p>
          <p class="mt-3 text-xs text-muted font-mono break-all">{{ version.id }}</p>
        </details>
      </div>
    </section>
    <UModal
      :open="editOpen"
      :title="t(isNew ? 'ruAddRule' : 'ruEditRule')"
      :description="t('ruEditorHelp')"
      :dismissible="!busy"
      :close="!busy"
      :ui="{
        content: 'max-w-[680px] max-h-[calc(100dvh-3rem)]',
        header: 'p-4 sm:p-6 shrink-0',
        body: 'p-4 sm:p-6 min-h-0 overflow-y-auto',
        footer: 'p-4 sm:p-6 shrink-0 justify-between',
      }"
      @update:open="requestClose"
    >
      <template #body
        ><div v-if="recoveryRequired || editorUncertain" class="mb-4 space-y-3">
          <UAlert
            color="warning"
            :title="t('mrUnknownTitle')"
            :description="t(recoveryRequired ? 'ruRecoveryHelp' : 'ruRecoveredHelp')"
          /><UButton v-if="recoveryRequired" :disabled="busy" @click="load(true)">{{
            t('mrReload')
          }}</UButton>
        </div>
        <UAlert v-if="error" color="error" :title="error" class="mb-4" />
        <form v-if="draft" id="rule-editor-form" @submit.prevent="persistRule()">
          <fieldset :disabled="blocked" class="space-y-5">
            <UFormField :label="t('memoryScope')" required
              ><USelect
                :model-value="draft.scope"
                :items="[
                  { label: t('rulesGlobal'), value: 'global' },
                  { label: t('memoryScene'), value: 'scene' },
                ]"
                class="w-full"
                @update:model-value="changeScope" /></UFormField
            ><UFormField v-if="draft.scope === 'scene'" :label="t('scConversation')" required
              ><USelect
                v-model="draft.sceneId"
                :items="scenes.map((scene) => ({ label: scene.name, value: scene.id }))"
                class="w-full"
              />
              <p v-if="!scenes.length" class="mt-2 text-xs text-muted">
                {{ t('mmNoScenes') }}
              </p></UFormField
            ><UFormField :label="t('rulesContent')" required
              ><UTextarea
                v-model="draft.content"
                :rows="7"
                maxlength="5000"
                required
                class="w-full" /></UFormField
            ><USwitch v-model="draft.enabled" :label="t('enabled')" /><UCheckbox
              v-model="draft.nonOverridable"
              :label="t('rulesNonOverridable')"
              :description="t('ruPriorityHelp')"
            />
          </fieldset></form
      ></template>
      <template #footer
        ><UButton
          v-if="!isNew"
          color="error"
          variant="ghost"
          :disabled="blocked"
          @click="ask('ruleDelete')"
          >{{ t('remove') }}</UButton
        ><span v-else />
        <div class="flex gap-3">
          <UButton color="neutral" variant="outline" :disabled="busy" @click="requestClose()">{{
            t('cancel')
          }}</UButton
          ><UButton
            type="submit"
            form="rule-editor-form"
            :loading="busy"
            :disabled="blocked || !valid || (!dirty && !isNew)"
            >{{ t('ruSaveRule') }}</UButton
          >
        </div></template
      >
    </UModal>
    <UModal
      v-model:open="hintOpen"
      :title="t('ruHintDetails')"
      :description="selectedHabit ? habitScope(selectedHabit) : ''"
      :dismissible="!busy"
      :close="!busy"
      :ui="{
        content: 'max-w-[680px] max-h-[calc(100dvh-3rem)]',
        header: 'p-4 sm:p-6 shrink-0',
        body: 'p-4 sm:p-6 min-h-0 overflow-y-auto',
        footer: 'p-4 sm:p-6 shrink-0 justify-between',
      }"
    >
      <template #body
        ><div v-if="recoveryRequired || editorUncertain" class="mb-4 space-y-3">
          <UAlert
            color="warning"
            :title="t('mrUnknownTitle')"
            :description="t(recoveryRequired ? 'ruRecoveryHelp' : 'ruRecoveredHelp')"
          /><UButton v-if="recoveryRequired" :disabled="busy" @click="load(true)">{{
            t('mrReload')
          }}</UButton>
        </div>
        <UAlert v-if="error" color="error" :title="error" class="mb-4" />
        <div v-if="selectedHabit" class="space-y-5">
          <p class="whitespace-pre-wrap break-words text-sm leading-7">
            {{ habitName(selectedHabit) }}
          </p>
          <div class="text-sm flex flex-wrap gap-3">
            <UBadge color="neutral" variant="subtle">{{
              t('ruStatus_' + selectedHabit.status)
            }}</UBadge
            ><span>{{ t('weight') }} {{ selectedHabit.weight.toFixed(2) }}</span
            ><span>{{ t('confidence') }} {{ Math.round(selectedHabit.confidence * 100) }}%</span
            ><span v-if="selectedHabit.locked">{{ t('mmLocked') }}</span>
          </div>
          <p class="text-xs text-muted leading-6">{{ t('ruWeightHelp') }} {{ t('ruLockHelp') }}</p>
          <p class="text-xs text-muted">
            {{ t('mmLearnedAt') }}: {{ date(selectedHabit.learnedAt) }}
          </p>
          <details class="border-t border-default pt-4">
            <summary class="cursor-pointer text-sm">
              {{ t('memorySources') }} · {{ selectedHabit.sources.length }}
            </summary>
            <p class="text-xs text-muted mt-3">{{ t('memorySourceWarning') }}</p>
            <ul class="mt-3 space-y-3">
              <li
                v-for="source in selectedHabit.sources"
                :key="JSON.stringify([source.sceneId, source.messageId])"
                class="text-xs break-all"
              >
                <p>{{ t('scSceneId') }}: {{ source.sceneId }}</p>
                <p>{{ t('mmMessageId') }}: {{ source.messageId }}</p>
                <p>{{ t('memoryPerson') }}: {{ source.senderId }}</p>
              </li>
            </ul>
          </details>
        </div></template
      >
      <template #footer
        ><div class="flex gap-2">
          <UButton color="error" variant="ghost" :disabled="blocked" @click="ask('habitDelete')">{{
            t('remove')
          }}</UButton
          ><UButton
            color="neutral"
            variant="outline"
            :disabled="blocked"
            @click="ask('habitLock')"
            >{{ t(selectedHabit?.locked ? 'unlock' : 'lock') }}</UButton
          >
        </div>
        <UButton :disabled="busy" @click="hintOpen = false">{{ t('scClose') }}</UButton></template
      >
    </UModal>
    <UModal
      :open="!!confirmation"
      :title="confirmationCopy.title"
      :description="confirmationCopy.help"
      :dismissible="!busy"
      :close="!busy"
      @update:open="
        (open) => {
          if (!open) cancelConfirmation()
        }
      "
      ><template #body
        ><div
          v-if="confirmationKind !== 'discard' && (recoveryRequired || editorUncertain)"
          class="mb-4 space-y-3"
        >
          <UAlert
            color="warning"
            :title="t('mrUnknownTitle')"
            :description="t(recoveryRequired ? 'ruRecoveryHelp' : 'ruRecoveredHelp')"
          /><UButton v-if="recoveryRequired" :disabled="busy" @click="load(true)">{{
            t('mrReload')
          }}</UButton>
        </div>
        <UAlert v-if="confirmationError" color="error" :title="confirmationError" />
        <p
          v-if="confirmationCopy.preview"
          class="text-sm leading-6 whitespace-pre-line break-words line-clamp-4"
        >
          {{ confirmationCopy.preview }}
        </p></template
      ><template #footer
        ><div class="flex justify-end gap-3 w-full">
          <UButton
            color="neutral"
            variant="outline"
            :disabled="busy || !confirmation"
            @click="cancelConfirmation"
            >{{ t(confirmationKind === 'discard' ? 'mcKeepEditing' : 'cancel') }}</UButton
          ><UButton
            :color="confirmationKind === 'habitLock' ? 'primary' : 'error'"
            :loading="busy"
            :disabled="busy || !confirmation || (confirmationKind !== 'discard' && blocked)"
            @click="confirm"
            >{{ confirmationCopy.action }}</UButton
          >
        </div></template
      ></UModal
    >
  </div>
</template>
