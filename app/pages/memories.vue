<script setup lang="ts">
import type { components } from '~/types/api'
type Memory = components['schemas']['Memory']
type Scene = components['schemas']['Scene']
const { t } = useI18n(),
  { client } = useApi()
const memories = ref<Memory[]>([]),
  scenes = ref<Scene[]>([]),
  error = ref(''),
  busy = ref(false),
  loaded = ref(false)
const sceneFilter = ref('all'),
  personFilter = ref(''),
  query = ref(''),
  editor = ref<'new' | string>(),
  deleteId = ref<string>(),
  publicConfirmed = ref(false)
const fresh = () => ({
  content: '',
  kind: 'fact' as Memory['kind'],
  importance: 'medium' as Memory['importance'],
  confidence: 1,
  locked: false,
  scope: 'scene',
  sceneId: '',
  personId: '',
  status: 'active' as Memory['status'],
})
const draft = ref(fresh())
watch(sceneFilter, () => {
  personFilter.value = ''
})
const sceneOptions = computed(() =>
  scenes.value.map((scene) => ({ label: scene.name, value: scene.id })),
)
const visible = computed(() =>
  memories.value.filter((memory) => {
    const scope = memory.scope
    return (
      (sceneFilter.value === 'all' ||
        (sceneFilter.value === 'public' && scope.kind === 'sharedPublicKnowledge') ||
        (scope.kind === 'scene' && scope.sceneId === sceneFilter.value)) &&
      (!personFilter.value ||
        (scope.kind === 'scene' && scope.personId === personFilter.value.trim())) &&
      (!query.value || memory.content.toLowerCase().includes(query.value.toLowerCase()))
    )
  }),
)
const canSave = computed(
  () =>
    !!draft.value.content.trim() &&
    (editor.value !== 'new' ||
      (draft.value.scope === 'public' ? publicConfirmed.value : !!draft.value.sceneId)),
)
async function load() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    const [records, places] = await Promise.all([
      client.GET('/api/memories'),
      client.GET('/api/scenes'),
    ])
    if (records.error || places.error) throw new Error(records.error?.error ?? places.error?.error)
    memories.value = records.data ?? []
    scenes.value = places.data ?? []
    loaded.value = true
  } catch {
    error.value = t('networkError')
  } finally {
    busy.value = false
  }
}
function begin(memory?: Memory) {
  if (busy.value) return
  deleteId.value = undefined
  publicConfirmed.value = false
  error.value = ''
  draft.value = memory
    ? {
        ...fresh(),
        content: memory.content,
        kind: memory.kind,
        importance: memory.importance,
        confidence: memory.confidence,
        locked: memory.locked,
        status: memory.status,
        scope: memory.scope.kind === 'scene' ? 'scene' : 'public',
        sceneId: memory.scope.kind === 'scene' ? memory.scope.sceneId : '',
        personId: memory.scope.kind === 'scene' ? (memory.scope.personId ?? '') : '',
      }
    : { ...fresh(), sceneId: sceneOptions.value[0]?.value ?? '' }
  editor.value = memory?.id ?? 'new'
}
function cancel() {
  if (!busy.value) {
    editor.value = undefined
    draft.value = fresh()
    publicConfirmed.value = false
  }
}
async function save() {
  if (busy.value || !canSave.value || !editor.value) return
  busy.value = true
  error.value = ''
  try {
    const response =
      editor.value === 'new'
        ? await client.POST('/api/memories', {
            body: {
              scope:
                draft.value.scope === 'public'
                  ? { kind: 'sharedPublicKnowledge' }
                  : {
                      kind: 'scene',
                      sceneId: draft.value.sceneId,
                      ...(draft.value.personId.trim()
                        ? { personId: draft.value.personId.trim() }
                        : {}),
                    },
              content: draft.value.content.trim(),
              kind: draft.value.kind,
              importance: draft.value.importance,
              confidence: Number(draft.value.confidence),
              locked: draft.value.locked,
            },
          })
        : await client.PATCH('/api/memories/{id}', {
            params: { path: { id: editor.value } },
            body: {
              content: draft.value.content.trim(),
              locked: draft.value.locked,
              status: draft.value.status,
            },
          })
    if (!response.data) {
      error.value = response.error?.error ?? t('networkError')
      return
    }
    memories.value = [
      response.data,
      ...memories.value.filter((memory) => memory.id !== response.data.id),
    ]
    editor.value = undefined
    draft.value = fresh()
    publicConfirmed.value = false
  } catch {
    error.value = t('networkError')
  } finally {
    busy.value = false
  }
}
async function toggleLock(memory: Memory) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    const response = await client.PATCH('/api/memories/{id}', {
      params: { path: { id: memory.id } },
      body: { locked: !memory.locked },
    })
    if (response.data)
      memories.value = memories.value.map((item) => (item.id === memory.id ? response.data : item))
    else error.value = response.error?.error ?? t('networkError')
  } catch {
    error.value = t('networkError')
  } finally {
    busy.value = false
  }
}
async function remove() {
  if (busy.value || !deleteId.value) return
  busy.value = true
  error.value = ''
  const id = deleteId.value
  try {
    const response = await client.DELETE('/api/memories/{id}', { params: { path: { id } } })
    if (response.data) {
      memories.value = memories.value.filter((memory) => memory.id !== id)
      deleteId.value = undefined
    } else error.value = response.error?.error ?? t('networkError')
  } catch {
    error.value = t('networkError')
  } finally {
    busy.value = false
  }
}

function scopeLabel(memory: Memory) {
  const scope = memory.scope
  return scope.kind === 'scene'
    ? (scenes.value.find((scene) => scene.id === scope.sceneId)?.name ?? scope.sceneId)
    : t('memoryPublic')
}
onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h1 class="text-2xl font-semibold">{{ t('memoryTitle') }}</h1>
        <p class="text-muted mt-1">{{ t('memoryHelp') }}</p>
      </div>
      <div class="flex gap-2">
        <UButton
          variant="ghost"
          icon="i-lucide-refresh-cw"
          :disabled="busy || !!editor"
          @click="load"
          >{{ t('refresh') }}</UButton
        ><UButton icon="i-lucide-plus" :disabled="busy || !!editor" @click="begin()">{{
          t('add')
        }}</UButton>
      </div>
    </header>
    <UAlert v-if="error" color="error" :title="error" />
    <UCard v-if="editor"
      ><template #header
        ><h2 class="font-semibold">
          {{ t(editor === 'new' ? 'memoryCreate' : 'memoryEdit') }}
        </h2></template
      >
      <form class="space-y-4" @submit.prevent="save">
        <fieldset :disabled="busy" class="space-y-4">
          <div v-if="editor === 'new'" class="grid sm:grid-cols-2 gap-4">
            <UFormField :label="t('memoryScope')"
              ><USelect
                v-model="draft.scope"
                :items="[
                  { label: t('memoryScene'), value: 'scene' },
                  { label: t('memoryPublic'), value: 'public' },
                ]"
                class="w-full" /></UFormField
            ><UFormField v-if="draft.scope === 'scene'" :label="t('scenes')" required
              ><USelect v-model="draft.sceneId" :items="sceneOptions" class="w-full" /></UFormField
            ><UFormField
              v-if="draft.scope === 'scene'"
              :label="t('memoryPerson')"
              :help="t('memoryPersonHelp')"
              ><UInput v-model="draft.personId" class="w-full"
            /></UFormField>
          </div>
          <UAlert
            v-if="editor === 'new' && draft.scope === 'public'"
            color="warning"
            :title="t('memoryPublicWarning')"
          /><UCheckbox
            v-if="editor === 'new' && draft.scope === 'public'"
            v-model="publicConfirmed"
            :label="t('memoryPublicConfirm')"
          />
          <UFormField :label="t('memoryContent')" required
            ><UTextarea v-model="draft.content" :rows="5" maxlength="10000" class="w-full"
          /></UFormField>
          <div v-if="editor === 'new'" class="grid sm:grid-cols-3 gap-4">
            <UFormField :label="t('memoryKind')"
              ><USelect
                v-model="draft.kind"
                :items="
                  ['fact', 'claim', 'term', 'experience', 'summary'].map((value) => ({
                    label: t(`memoryKind_${value}`),
                    value,
                  }))
                "
                class="w-full" /></UFormField
            ><UFormField :label="t('memoryImportance')"
              ><USelect
                v-model="draft.importance"
                :items="
                  ['low', 'medium', 'high'].map((value) => ({
                    label: t(`memoryImportance_${value}`),
                    value,
                  }))
                "
                class="w-full" /></UFormField
            ><UFormField :label="t('memoryConfidence')"
              ><UInput
                v-model.number="draft.confidence"
                type="number"
                min="0"
                max="1"
                step="any"
                class="w-full"
            /></UFormField>
          </div>
          <UFormField v-else :label="t('memoryStatus')"
            ><USelect
              v-model="draft.status"
              :items="
                ['active', 'disputed', 'superseded', 'invalidated'].map((value) => ({
                  label: t(`memoryStatus_${value}`),
                  value,
                }))
              "
              class="w-full"
          /></UFormField>
          <USwitch v-model="draft.locked" :label="t('memoryLocked')" />
          <div class="flex gap-2">
            <UButton type="submit" :loading="busy" :disabled="!canSave">{{ t('save') }}</UButton
            ><UButton variant="ghost" :disabled="busy" @click="cancel">{{ t('cancel') }}</UButton>
          </div>
        </fieldset>
      </form>
    </UCard>
    <div class="grid sm:grid-cols-3 gap-3">
      <USelect
        v-model="sceneFilter"
        :aria-label="t('memoryScope')"
        :items="[
          { label: t('memoryAllScenes'), value: 'all' },
          { label: t('memoryPublic'), value: 'public' },
          ...sceneOptions,
        ]"
        class="w-full"
      /><UInput
        v-model="personFilter"
        :disabled="sceneFilter === 'all' || sceneFilter === 'public'"
        :placeholder="t('memoryPersonFilter')"
        class="w-full"
      /><UInput
        v-model="query"
        icon="i-lucide-search"
        :placeholder="t('memorySearch')"
        class="w-full"
      />
    </div>
    <p v-if="loaded && !visible.length" class="text-muted py-8 text-center">
      {{ t('memoryEmpty') }}
    </p>
    <UCard v-for="memory in visible" :key="memory.id"
      ><div class="space-y-4">
        <div class="flex flex-wrap gap-2">
          <UBadge variant="soft">{{ scopeLabel(memory) }}</UBadge
          ><UBadge color="neutral" variant="outline">{{
            t(`memoryStatus_${memory.status}`)
          }}</UBadge
          ><UBadge color="neutral" variant="soft">{{ t(`memoryKind_${memory.kind}`) }}</UBadge
          ><span
            v-if="memory.scope.kind === 'scene' && memory.scope.personId"
            class="text-xs text-muted break-all"
            >{{ t('memoryPerson') }}: {{ memory.scope.personId }}</span
          >
        </div>
        <p class="whitespace-pre-wrap break-words">{{ memory.content }}</p>
        <div class="flex flex-wrap justify-between items-center gap-3">
          <span class="text-xs text-muted"
            >{{ t('memoryConfidence') }} {{ Math.round(memory.confidence * 100) }}% ·
            {{ t(`memoryOrigin_${memory.origin}`) }} ·
            {{ new Date(memory.learnedAt).toLocaleString() }}</span
          >
          <div class="flex gap-1">
            <UButton
              size="sm"
              variant="ghost"
              :disabled="busy || !!editor"
              @click="begin(memory)"
              >{{ t('edit') }}</UButton
            ><UButton
              size="sm"
              variant="ghost"
              :icon="memory.locked ? 'i-lucide-lock' : 'i-lucide-lock-open'"
              :disabled="busy || !!editor"
              @click="toggleLock(memory)"
              >{{ t(memory.locked ? 'memoryUnlock' : 'memoryLock') }}</UButton
            ><UButton
              size="sm"
              variant="ghost"
              color="error"
              :disabled="busy || !!editor"
              @click="deleteId = memory.id"
              >{{ t('remove') }}</UButton
            >
          </div>
        </div>
        <details v-if="memory.sources.length" class="text-xs text-muted">
          <summary class="cursor-pointer">
            {{ t('memorySources') }} ({{ memory.sources.length }})
          </summary>
          <ul class="mt-2 space-y-1">
            <li
              v-for="source in memory.sources"
              :key="`${source.sceneId}:${source.messageId}`"
              class="break-all"
            >
              {{ source.sceneId }} / {{ source.messageId }} / {{ source.senderId }}
            </li>
          </ul>
          <p class="mt-2">{{ t('memorySourceWarning') }}</p>
        </details>
        <div
          v-if="deleteId === memory.id"
          class="rounded-lg border border-error p-3 flex flex-wrap items-center gap-3"
        >
          <p class="flex-1 text-sm">{{ t('memoryDeleteConfirm') }}</p>
          <UButton color="error" size="sm" :loading="busy" @click="remove">{{
            t('confirmDelete')
          }}</UButton
          ><UButton variant="ghost" size="sm" :disabled="busy" @click="deleteId = undefined">{{
            t('cancel')
          }}</UButton>
        </div>
      </div></UCard
    >
  </div>
</template>
