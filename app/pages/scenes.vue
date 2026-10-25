<script setup lang="ts">
import type { components } from '~/types/api'
const { t } = useI18n(),
  { client } = useApi()
const { data: result, refresh } = await useAsyncData('scenes', () => client.GET('/api/scenes'), {
  server: false,
})
const error = ref('')
const states = ref<Record<string, components['schemas']['SceneState']>>({})
async function inspect(id: string) {
  try {
    const r = await client.GET('/api/scenes/{id}/state', { params: { path: { id } } })
    if (r.data) states.value[id] = r.data
    else error.value = r.error?.error ?? t('networkError')
  } catch {
    error.value = t('networkError')
  }
}

async function save(
  scene: NonNullable<typeof result.value>['data'] extends (infer T)[] | undefined ? T : never,
) {
  const r = await client.PUT('/api/scenes/{id}', {
    params: { path: { id: scene.id } },
    body: scene,
  })
  error.value = r.error?.error ?? ''
  await refresh()
}
</script>
<template>
  <div class="space-y-6">
    <h1 class="text-2xl font-semibold">{{ t('scenes') }}</h1>
    <p class="text-muted">{{ t('scenesHelp') }}</p>
    <UAlert v-if="error" color="error" :title="error" /><UCard v-if="!result?.data?.length"
      ><p class="text-muted">{{ t('emptyScenes') }}</p></UCard
    ><UCard v-for="scene in result?.data ?? []" :key="scene.id"
      ><template #header
        ><div class="flex justify-between">
          <h2 class="font-semibold">{{ scene.name }}</h2>
          <UBadge color="neutral">{{ scene.kind }}</UBadge>
        </div></template
      >
      <div class="flex flex-wrap gap-6">
        <USwitch
          v-model="scene.participationEnabled"
          :label="t('participation')"
          @update:model-value="save(scene)"
        /><USwitch
          v-model="scene.learningEnabled"
          :label="t('learning')"
          @update:model-value="save(scene)"
        /><USwitch
          v-if="scene.kind === 'group'"
          v-model="scene.proactiveEnabled"
          :label="t('proactive')"
          @update:model-value="save(scene)"
        />
      </div>
      <div class="mt-5">
        <UButton variant="soft" size="sm" @click="inspect(scene.id)">{{
          t('inspectTransientState')
        }}</UButton>
        <div v-if="states[scene.id]" class="mt-3 text-sm text-muted">
          <p>{{ t('transientStateHelp') }}</p>
          <p class="mt-2">{{ t('activeTasks') }}: {{ states[scene.id]!.activeTasks }}</p>
          <div v-for="topic in states[scene.id]!.topics" :key="topic.id" class="mt-2">
            <code>{{ topic.id }}</code> · {{ t('attention') }}: {{ topic.attention.toFixed(2) }} ·
            {{ t('messageCount') }}: {{ topic.messageCount }}
          </div>
        </div>
      </div></UCard
    >
  </div>
</template>
