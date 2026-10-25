<script setup lang="ts">
const {t}=useI18n(),{client}=useApi()
const {data:result,refresh}=await useAsyncData('scenes',()=>client.GET('/api/scenes'),{server:false})
const error=ref('')
async function save(scene:NonNullable<typeof result.value>['data'] extends (infer T)[]|undefined?T:never){const r=await client.PUT('/api/scenes/{id}',{params:{path:{id:scene.id}},body:scene});error.value=r.error?.error??'';await refresh()}
</script>
<template><div class="space-y-6"><h1 class="text-2xl font-semibold">{{t('scenes')}}</h1><p class="text-muted">{{t('scenesHelp')}}</p><UAlert v-if="error" color="error" :title="error"/><UCard v-if="!result?.data?.length"><p class="text-muted">{{t('emptyScenes')}}</p></UCard><UCard v-for="scene in result?.data??[]" :key="scene.id"><template #header><div class="flex justify-between"><h2 class="font-semibold">{{scene.name}}</h2><UBadge color="neutral">{{scene.kind}}</UBadge></div></template><div class="flex flex-wrap gap-6"><USwitch v-model="scene.participationEnabled" :label="t('participation')" @update:model-value="save(scene)"/><USwitch v-model="scene.learningEnabled" :label="t('learning')" @update:model-value="save(scene)"/><USwitch v-if="scene.kind==='group'" v-model="scene.proactiveEnabled" :label="t('proactive')" @update:model-value="save(scene)"/></div></UCard></div></template>
