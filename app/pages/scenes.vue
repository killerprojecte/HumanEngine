<script setup lang="ts">
import type { components } from '~/types/api'
import { humanName } from '~/utils/chatIdentity'
type Scene = components['schemas']['Scene'] & { avatarUrl?: string }
type SceneState = components['schemas']['SceneState']
type SwitchField =
  | 'participationEnabled'
  | 'learningEnabled'
  | 'proactiveEnabled'
  | 'memoryReadEnabled'
  | 'personalizationEnabled'
const { t } = useI18n(),
  { client } = useApi()
const scenes = ref<Scene[]>([]),
  loading = ref(false),
  loaded = ref(false),
  error = ref('')
const pending = ref<Record<string, boolean>>({}),
  rowErrors = ref<Record<string, string>>({})
const savedScenes = new Map<string, Scene>()
const query = ref(''),
  kind = ref('all')
const anyPending = computed(() => Object.values(pending.value).some(Boolean))
const visible = computed(() =>
  scenes.value.filter(
    (scene) =>
      (kind.value === 'all' || scene.kind === kind.value) &&
      [scene.name, scene.adapterId, scene.platformSceneId]
        .join(' ')
        .toLocaleLowerCase()
        .includes(query.value.trim().toLocaleLowerCase()),
  ),
)
const sceneName = (scene: Scene) =>
  humanName(scene.name) ||
  t(scene.kind === 'group' ? 'cwUnnamedGroup' : 'cwUnnamedPrivate', {
    number: scenes.value.findIndex((item) => item.id === scene.id) + 1,
  })
const kindLabel = (value: Scene['kind']) => t(value === 'group' ? 'scGroup' : 'scPrivate')
const selectedId = ref(''),
  detailOpen = ref(false),
  state = ref<SceneState>(),
  detailError = ref(''),
  inspecting = ref(false)
const selected = computed(() => scenes.value.find((scene) => scene.id === selectedId.value))
let disposed = false,
  inspectionGeneration = 0
let inspectionController: AbortController | undefined
async function load() {
  if (loading.value || anyPending.value || disposed) return
  loading.value = true
  error.value = ''
  try {
    const result = await client.GET('/api/scenes')
    if (disposed) return
    if (!result.data) throw new Error(result.error?.error ?? t('networkError'))
    scenes.value = result.data
    savedScenes.clear()
    for (const scene of result.data) savedScenes.set(scene.id, { ...scene })
    rowErrors.value = {}
    loaded.value = true
  } catch (cause) {
    if (!disposed) error.value = cause instanceof Error ? cause.message : t('networkError')
  } finally {
    if (!disposed) loading.value = false
  }
}
function replaceScene(scene: Scene) {
  scenes.value = scenes.value.map((current) => (current.id === scene.id ? scene : current))
}
async function update(scene: Scene, field: SwitchField, enabled: boolean) {
  if (
    disposed ||
    loading.value ||
    pending.value[scene.id] ||
    !!rowErrors.value[scene.id] ||
    (field === 'proactiveEnabled' && scene.kind !== 'group')
  )
    return
  const saved = savedScenes.get(scene.id)
  if (
    !saved ||
    (field === 'memoryReadEnabled' || field === 'personalizationEnabled'
      ? saved[field] !== false
      : saved[field]) === enabled ||
    (field === 'proactiveEnabled' && !saved.participationEnabled)
  )
    return
  const previous = { ...saved },
    next = { ...saved, [field]: enabled }
  pending.value[scene.id] = true
  rowErrors.value[scene.id] = ''
  replaceScene(next)
  try {
    const result = await client.PUT('/api/scenes/{id}', {
      params: { path: { id: scene.id } },
      body: next,
    })
    if (disposed) return
    if (!result.data) throw new Error(result.error?.error ?? t('networkError'))
    savedScenes.set(scene.id, { ...result.data })
    replaceScene(result.data)
  } catch (cause) {
    if (!disposed) {
      replaceScene(previous)
      rowErrors.value[scene.id] =
        t('scSaveFailed') + ' ' + (cause instanceof Error ? cause.message : t('networkError'))
    }
  } finally {
    if (!disposed) pending.value[scene.id] = false
  }
}
async function inspect(id: string) {
  inspectionController?.abort()
  const generation = ++inspectionGeneration
  inspectionController = new AbortController()
  selectedId.value = id
  detailOpen.value = true
  state.value = undefined
  detailError.value = ''
  inspecting.value = true
  try {
    const result = await client.GET('/api/scenes/{id}/state', {
      params: { path: { id } },
      signal: inspectionController.signal,
    })
    if (disposed || generation !== inspectionGeneration) return
    if (result.data) state.value = result.data
    else detailError.value = result.error?.error ?? t('networkError')
  } catch {
    if (!disposed && generation === inspectionGeneration) detailError.value = t('networkError')
  } finally {
    if (!disposed && generation === inspectionGeneration) inspecting.value = false
  }
}
watch(detailOpen, (open) => {
  if (!open) {
    ++inspectionGeneration
    inspectionController?.abort()
    inspecting.value = false
    state.value = undefined
  }
})
onMounted(load)
onBeforeUnmount(() => {
  disposed = true
  ++inspectionGeneration
  inspectionController?.abort()
})
</script>
<template>
  <div class="space-y-5">
    <header class="flex items-start justify-between gap-4">
      <div>
        <h1 class="page-heading">{{ t('scTitle') }}</h1>
        <p class="mt-2 text-sm text-muted">{{ t('scHelp') }}</p>
      </div>
      <UButton
        variant="outline"
        color="neutral"
        icon="i-lucide-refresh-cw"
        :loading="loading"
        :disabled="loading || anyPending"
        @click="load"
        >{{ t('refresh') }}</UButton
      >
    </header>
    <UAlert v-if="error" color="error" :title="error" />
    <section class="rounded-lg border border-default bg-default overflow-hidden">
      <div class="flex flex-wrap gap-3 p-4 border-b border-default">
        <UInput
          v-model="query"
          icon="i-lucide-search"
          :placeholder="t('scSearch')"
          :aria-label="t('scSearch')"
          class="w-full sm:w-80"
        />
        <USelect
          v-model="kind"
          :aria-label="t('scKind')"
          :items="[
            { label: t('scAllKinds'), value: 'all' },
            { label: t('scGroup'), value: 'group' },
            { label: t('scPrivate'), value: 'private' },
          ]"
          class="w-full sm:w-44"
        />
      </div>
      <p class="px-4 py-3 text-xs leading-5 text-muted border-b border-default">
        {{ t('scImmediateHelp') }} {{ t('scMemoryReadHelp') }}
      </p>
      <p v-if="!loaded && loading" class="px-4 py-10 text-center text-muted">
        {{ t('uxLoading') }}
      </p>
      <p v-else-if="loaded && !visible.length" class="px-4 py-10 text-center text-muted">
        {{ t(scenes.length ? 'scNoMatches' : 'emptyScenes') }}
      </p>
      <template v-else>
        <table class="scene-table hidden lg:table w-full table-fixed text-left text-sm">
          <colgroup>
            <col style="width: 28%" />
            <col style="width: 12%" />
            <col style="width: 12%" />
            <col style="width: 12%" />
            <col style="width: 12%" />
            <col style="width: 12%" />
            <col style="width: 12%" />
          </colgroup>
          <thead>
            <tr>
              <th>{{ t('scConversation') }}</th>
              <th>{{ t('scReplies') }}</th>
              <th>{{ t('scLearning') }}</th>
              <th>{{ t('scProactive') }}</th>
              <th>{{ t('scMemoryRead') }}</th>
              <th>{{ t('scPersonalization') }}</th>
              <th class="text-right">{{ t('scDetails') }}</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="scene in visible" :key="scene.id"
              ><tr>
                <td>
                  <div class="flex items-center gap-3">
                    <ChatAvatar
                      :name="sceneName(scene)"
                      :identity="scene.id"
                      :src="scene.avatarUrl"
                      :group="scene.kind === 'group'"
                    /><span class="font-medium break-words">{{ sceneName(scene) }}</span>
                  </div>
                  <p class="mt-1.5 text-xs text-muted">{{ kindLabel(scene.kind) }}</p>
                </td>
                <td>
                  <USwitch
                    :model-value="scene.participationEnabled"
                    :disabled="loading || pending[scene.id] || !!rowErrors[scene.id]"
                    :aria-label="t('scReplies') + ': ' + sceneName(scene)"
                    @update:model-value="(value) => update(scene, 'participationEnabled', value)"
                  />
                </td>
                <td>
                  <USwitch
                    :model-value="scene.learningEnabled"
                    :disabled="loading || pending[scene.id] || !!rowErrors[scene.id]"
                    :aria-label="t('scLearning') + ': ' + sceneName(scene)"
                    @update:model-value="(value) => update(scene, 'learningEnabled', value)"
                  />
                </td>
                <td>
                  <USwitch
                    v-if="scene.kind === 'group'"
                    :model-value="scene.proactiveEnabled"
                    :disabled="
                      loading ||
                      pending[scene.id] ||
                      !!rowErrors[scene.id] ||
                      !scene.participationEnabled
                    "
                    :aria-label="t('scProactive') + ': ' + sceneName(scene)"
                    @update:model-value="(value) => update(scene, 'proactiveEnabled', value)"
                  /><span v-else class="text-xs text-muted">{{ t('scPassiveOnly') }}</span>
                </td>
                <td>
                  <USwitch
                    :model-value="scene.memoryReadEnabled !== false"
                    :disabled="loading || pending[scene.id] || !!rowErrors[scene.id]"
                    :aria-label="t('scMemoryRead') + ': ' + sceneName(scene)"
                    @update:model-value="(value) => update(scene, 'memoryReadEnabled', value)"
                  />
                </td>
                <td>
                  <USwitch
                    :model-value="scene.personalizationEnabled !== false"
                    :disabled="loading || pending[scene.id] || !!rowErrors[scene.id]"
                    :aria-label="t('scPersonalization') + ': ' + sceneName(scene)"
                    @update:model-value="(value) => update(scene, 'personalizationEnabled', value)"
                  />
                </td>
                <td class="text-right">
                  <UButton variant="ghost" color="neutral" @click="inspect(scene.id)">{{
                    t('scDetails')
                  }}</UButton>
                </td>
              </tr>
              <tr
                v-if="
                  rowErrors[scene.id] ||
                  pending[scene.id] ||
                  (scene.kind === 'group' && !scene.participationEnabled)
                "
              >
                <td colspan="7" class="status-cell">
                  <p
                    v-if="scene.kind === 'group' && !scene.participationEnabled"
                    class="text-muted mb-1"
                  >
                    {{ t('scProactive') }} · {{ t('scProactivePaused') }}
                  </p>
                  <p
                    v-if="rowErrors[scene.id] || pending[scene.id]"
                    role="status"
                    :class="rowErrors[scene.id] ? 'text-error' : 'text-muted'"
                  >
                    {{ rowErrors[scene.id] || t('scSaving') }}
                  </p>
                </td>
              </tr></template
            >
          </tbody>
        </table>
        <div class="lg:hidden divide-y divide-default">
          <article v-for="scene in visible" :key="scene.id" class="p-4 space-y-4">
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0">
                <div class="flex items-center gap-3">
                  <ChatAvatar
                    :name="sceneName(scene)"
                    :identity="scene.id"
                    :src="scene.avatarUrl"
                    :group="scene.kind === 'group'"
                  />
                  <h2 class="font-medium break-words">{{ sceneName(scene) }}</h2>
                </div>
                <p class="mt-1 text-xs text-muted">{{ kindLabel(scene.kind) }}</p>
              </div>
              <UButton
                class="shrink-0"
                variant="ghost"
                color="neutral"
                @click="inspect(scene.id)"
                >{{ t('scDetails') }}</UButton
              >
            </div>
            <div class="flex flex-col items-start gap-3">
              <USwitch
                :model-value="scene.participationEnabled"
                :label="t('scReplies')"
                :disabled="loading || pending[scene.id] || !!rowErrors[scene.id]"
                @update:model-value="(value) => update(scene, 'participationEnabled', value)"
              /><USwitch
                :model-value="scene.learningEnabled"
                :label="t('scLearning')"
                :disabled="loading || pending[scene.id] || !!rowErrors[scene.id]"
                @update:model-value="(value) => update(scene, 'learningEnabled', value)"
              /><USwitch
                v-if="scene.kind === 'group'"
                :model-value="scene.proactiveEnabled"
                :label="t('scProactive')"
                :disabled="
                  loading ||
                  pending[scene.id] ||
                  !!rowErrors[scene.id] ||
                  !scene.participationEnabled
                "
                @update:model-value="(value) => update(scene, 'proactiveEnabled', value)"
              />
              <p v-else class="text-xs text-muted">{{ t('scPassiveOnly') }}</p>
              <USwitch
                :model-value="scene.memoryReadEnabled !== false"
                :label="t('scMemoryRead')"
                :disabled="loading || pending[scene.id] || !!rowErrors[scene.id]"
                @update:model-value="(value) => update(scene, 'memoryReadEnabled', value)"
              />
              <USwitch
                :model-value="scene.personalizationEnabled !== false"
                :label="t('scPersonalization')"
                :disabled="loading || pending[scene.id] || !!rowErrors[scene.id]"
                @update:model-value="(value) => update(scene, 'personalizationEnabled', value)"
              />
              <p
                v-if="scene.kind === 'group' && !scene.participationEnabled"
                class="text-xs text-muted"
              >
                {{ t('scProactivePaused') }}
              </p>
            </div>
            <p
              v-if="rowErrors[scene.id] || pending[scene.id]"
              role="status"
              class="text-xs"
              :class="rowErrors[scene.id] ? 'text-error' : 'text-muted'"
            >
              {{ rowErrors[scene.id] || t('scSaving') }}
            </p>
          </article>
        </div>
      </template>
    </section>
    <UModal
      v-model:open="detailOpen"
      :title="selected ? sceneName(selected) : t('scDetails')"
      :description="t('scTemporaryHelp')"
      :ui="{
        content: 'max-w-[680px] max-h-[calc(100dvh-3rem)]',
        header: 'p-4 sm:p-6 shrink-0',
        body: 'p-4 sm:p-6 min-h-0 overflow-y-auto',
        footer: 'p-4 sm:p-6 shrink-0 justify-end',
      }"
    >
      <template #body
        ><div class="space-y-5">
          <UAlert v-if="detailError" color="error" :title="detailError" />
          <p v-if="inspecting" class="text-sm text-muted">{{ t('uxLoading') }}</p>
          <template v-if="state">
            <dl class="grid sm:grid-cols-2 gap-4 text-sm">
              <div>
                <dt class="text-muted">{{ t('activeTasks') }}</dt>
                <dd class="mt-1 font-medium">{{ state.activeTasks }}</dd>
              </div>
              <div>
                <dt class="text-muted">{{ t('scSinceReply') }}</dt>
                <dd class="mt-1 font-medium">
                  {{
                    state.secondsSinceBotSpoke === null
                      ? t('scNoReply')
                      : t('scSeconds', { count: Math.floor(state.secondsSinceBotSpoke) })
                  }}
                </dd>
              </div>
            </dl>
            <div class="text-sm text-muted space-y-1">
              <p>
                {{
                  t('topicRoutingStatus', {
                    enabled: state.topicRouting.enabled ? t('enabled') : t('disabled'),
                    deferred: state.topicRouting.deferred,
                    overflowed: state.topicRouting.overflowed,
                    provisional: state.topicRouting.provisional,
                  })
                }}
              </p>
              <p>{{ t('topicRoutingLimits') }}</p>
            </div>
            <section>
              <h3 class="text-sm font-medium mb-3">
                {{ t('scTopics') }} ({{ state.topics.length }})
              </h3>
              <p v-if="!state.topics.length" class="text-sm text-muted">{{ t('scNoTopics') }}</p>
              <div
                v-for="topic in state.topics"
                :key="topic.id"
                class="border-t border-default py-3"
              >
                <code class="text-xs break-all">{{ topic.id }}</code>
                <dl class="mt-2 flex flex-wrap gap-x-5 gap-y-2 text-xs text-muted">
                  <div>
                    <dt class="inline">{{ t('attention') }}:</dt>
                    <dd class="inline">{{ topic.attention.toFixed(2) }}</dd>
                  </div>
                  <div>
                    <dt class="inline">{{ t('messageCount') }}:</dt>
                    <dd class="inline">{{ topic.messageCount }}</dd>
                  </div>
                  <div>
                    <dt class="inline">{{ t('scTopicAge') }}:</dt>
                    <dd class="inline">
                      {{ t('scSeconds', { count: Math.floor(topic.ageMs / 1000) }) }}
                    </dd>
                  </div>
                </dl>
              </div>
            </section>
          </template>
          <details v-if="selected" class="border-t border-default pt-4">
            <summary class="cursor-pointer text-sm text-muted">{{ t('scIdentifiers') }}</summary>
            <dl class="space-y-3 mt-3 text-xs">
              <div
                v-for="item in [
                  { label: t('scSceneId'), value: selected.id },
                  { label: t('scAdapterId'), value: selected.adapterId },
                  { label: t('scAccountId'), value: selected.accountId },
                  { label: t('scPlatformId'), value: selected.platformSceneId },
                ]"
                :key="item.label"
              >
                <dt class="text-muted">{{ item.label }}</dt>
                <dd class="font-mono break-all mt-1">{{ item.value }}</dd>
              </div>
            </dl>
          </details>
        </div></template
      >
      <template #footer
        ><UButton
          variant="outline"
          color="neutral"
          :loading="inspecting"
          :disabled="inspecting || !selected"
          @click="selected && inspect(selected.id)"
          >{{ t('refresh') }}</UButton
        ><UButton @click="detailOpen = false">{{ t('scClose') }}</UButton></template
      >
    </UModal>
  </div>
</template>
<style scoped>
.scene-table th {
  height: 44px;
  padding: 0 16px;
  font-size: 12px;
  font-weight: 500;
  color: var(--ui-text-muted);
  background: var(--ui-bg-muted);
}
.scene-table td {
  padding: 16px;
  vertical-align: middle;
  border-top: 1px solid var(--ui-border);
}
.scene-table .status-cell {
  padding-top: 0;
  padding-bottom: 12px;
  border-top: 0;
  font-size: 12px;
}
</style>
