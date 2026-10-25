<script setup lang="ts">
import type { components } from '~/types/api'
type Meme = components['schemas']['Meme']
const { t } = useI18n(),
  { client } = useApi(),
  assets = ref<Meme[]>([]),
  images = ref<Record<string, string>>({}),
  error = ref(''),
  busy = ref(''),
  selected = ref<Meme>(),
  editOpen = ref(false),
  tagText = ref<Record<string, string>>({}),
  ocr = ref('')
const categories = [
  'appearance',
  'style',
  'subject',
  'emotion',
  'intent',
  'tone',
  'intensity',
  'memeMeaning',
] as const
async function load() {
  const r = await client.GET('/api/memes')
  error.value = r.error?.error ?? ''
  assets.value = r.data ?? []
  for (const asset of assets.value.slice(0, 60)) {
    if (images.value[asset.id]) continue
    const image = await client.GET('/api/memes/{id}/file', {
      params: { path: { id: asset.id } },
      parseAs: 'blob',
    })
    if (image.data) images.value[asset.id] = URL.createObjectURL(image.data)
  }
}
onMounted(() => load().catch(() => (error.value = t('networkError'))))
onBeforeUnmount(() => Object.values(images.value).forEach((url) => URL.revokeObjectURL(url)))
async function patch(
  asset: Meme,
  body: {
    available?: boolean
    shared?: boolean
    privacyReviewed?: boolean
    favorite?: boolean
    disabled?: boolean
    tags?: Meme['tags']
  },
) {
  if (busy.value) return
  busy.value = asset.id
  error.value = ''
  try {
    const r = await client.PATCH('/api/memes/{id}', { params: { path: { id: asset.id } }, body })
    if (r.error) error.value = r.error.error
    else await load()
  } catch {
    error.value = t('networkError')
  } finally {
    busy.value = ''
  }
}
async function analyze(asset: Meme) {
  const source = asset.sources[0]
  if (!source) return
  busy.value = asset.id
  try {
    const r = await client.POST('/api/memes/{id}/analyze', {
      params: { path: { id: asset.id } },
      body: { sceneId: source.sceneId },
    })
    error.value = r.error?.error ?? ''
    await load()
  } catch {
    error.value = t('networkError')
  } finally {
    busy.value = ''
  }
}
function edit(asset: Meme) {
  selected.value = asset
  for (const key of categories) tagText.value[key] = asset.tags[key].join(', ')
  ocr.value = asset.tags.ocr
  editOpen.value = true
}
async function saveTags() {
  if (!selected.value) return
  const tags = { ...selected.value.tags, ocr: ocr.value }
  for (const key of categories)
    tags[key] = (tagText.value[key] ?? '')
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
  await patch(selected.value, { tags })
  if (!error.value) editOpen.value = false
}
</script>
<template>
  <div class="space-y-6">
    <div class="flex justify-between">
      <h1 class="text-2xl font-semibold">{{ t('memes') }}</h1>
      <UButton variant="soft" icon="i-lucide-refresh-cw" @click="load">{{ t('refresh') }}</UButton>
    </div>
    <UAlert
      color="neutral"
      icon="i-lucide-shield"
      :title="t('memePrivacy')"
      :description="t('memePrivacyHelp')"
    /><UAlert v-if="error" color="error" :title="error" /><UCard v-if="!assets.length"
      ><p class="text-muted">{{ t('emptyMemes') }}</p></UCard
    >
    <div class="grid sm:grid-cols-2 xl:grid-cols-3 gap-5">
      <UCard v-for="asset in assets" :key="asset.id"
        ><div class="h-44 bg-muted rounded-xl flex items-center justify-center mb-4">
          <img
            v-if="images[asset.id]"
            :src="images[asset.id]"
            :alt="asset.tags.intent.join(', ') || t('media')"
            class="max-h-full max-w-full object-contain rounded-lg"
          /><UIcon v-else name="i-lucide-image" class="text-4xl text-muted" />
        </div>
        <div class="flex gap-2 flex-wrap mb-4">
          <UBadge color="neutral">{{ asset.format }} · {{ asset.frames }} {{ t('frames') }}</UBadge
          ><UBadge :color="asset.shared ? 'warning' : 'neutral'">{{
            asset.shared ? t('shared') : t('sceneOnly')
          }}</UBadge
          ><UBadge
            v-for="tag in [...asset.tags.intent, ...asset.tags.style].slice(0, 4)"
            :key="tag"
            variant="soft"
            >{{ tag }}</UBadge
          >
        </div>
        <div class="space-y-3">
          <USwitch
            :model-value="asset.available"
            :disabled="!!busy"
            :label="t('available')"
            @update:model-value="(value) => patch(asset, { available: value })"
          /><USwitch
            :model-value="asset.disabled"
            :disabled="!!busy"
            :label="t('disabled')"
            @update:model-value="(value) => patch(asset, { disabled: value })"
          /><UCheckbox
            :model-value="asset.shared && asset.privacyReviewed"
            :disabled="!!busy"
            :label="t('reviewedSharing')"
            @update:model-value="
              (value) => patch(asset, { shared: value === true, privacyReviewed: value === true })
            "
          />
          <div class="flex gap-2 flex-wrap">
            <UButton
              :icon="asset.favorite ? 'i-lucide-star' : 'i-lucide-star'"
              :color="asset.favorite ? 'warning' : 'neutral'"
              variant="soft"
              :disabled="!!busy"
              @click="patch(asset, { favorite: !asset.favorite })"
              >{{ t('favorite') }}</UButton
            ><UButton variant="ghost" :disabled="!!busy" @click="edit(asset)">{{
              t('editTags')
            }}</UButton
            ><UButton
              variant="ghost"
              :loading="busy === asset.id"
              :disabled="!!busy || !asset.sources.length"
              @click="analyze(asset)"
              >{{ t('reanalyze') }}</UButton
            >
          </div>
          <p v-if="asset.analysisError" class="text-xs text-error">{{ asset.analysisError }}</p>
        </div></UCard
      >
    </div>
    <UModal v-model:open="editOpen" :title="t('editTags')"
      ><template #body
        ><div class="space-y-4">
          <UFormField v-for="category in categories" :key="category" :label="category"
            ><UInput
              v-model="tagText[category]"
              class="w-full"
              :placeholder="t('commaTags')" /></UFormField
          ><UFormField label="OCR"
            ><UTextarea v-model="ocr" class="w-full"
          /></UFormField></div></template
      ><template #footer
        ><UButton variant="ghost" @click="editOpen = false">{{ t('cancel') }}</UButton
        ><UButton :loading="!!busy" @click="saveTags">{{ t('save') }}</UButton></template
      ></UModal
    >
  </div>
</template>
