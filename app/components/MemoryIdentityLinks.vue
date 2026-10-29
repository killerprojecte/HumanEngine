<script setup lang="ts">
import type { components } from '~/types/api'
type Endpoint = { adapterId: string; accountId: string; personId: string }
type Alias = {
  id: string
  endpoint: Endpoint
  label: string
  scopeSceneId: string
  validFrom: string | null
  validUntil: string | null
}
type Registry = { revision: number; edges: [Endpoint, Endpoint][]; aliases: Alias[] }
const props = defineProps<{ scenes: components['schemas']['Scene'][] }>()
const { t } = useI18n()
const { authenticated, authenticatedFetch } = useApi()
const expanded = ref(false),
  busy = ref(false),
  error = ref(false)
const data = ref<Registry>()
const left = reactive<Endpoint>({ adapterId: '', accountId: '', personId: '' })
const right = reactive<Endpoint>({ adapterId: '', accountId: '', personId: '' })
const sceneId = ref(''),
  aliasLabel = ref(''),
  validFrom = ref(''),
  validUntil = ref('')
let controller: AbortController | undefined,
  epoch = 0
const scenes = computed(() => props.scenes.filter((s) => ['group', 'private'].includes(s.kind)))
const endpointLabel = (e: Endpoint) => `${e.adapterId} / ${e.accountId} / ${e.personId}`
function clear() {
  epoch++
  controller?.abort()
  data.value = undefined
  error.value = false
  busy.value = false
}
async function load() {
  clear()
  if (!expanded.value || !authenticated.value) return
  const mine = epoch
  controller = new AbortController()
  busy.value = true
  try {
    const r = await authenticatedFetch(new URL('/api/memory-identities', location.origin), {
      signal: controller.signal,
    })
    if (!r.ok) throw new Error('load')
    const value = (await r.json()) as Registry
    if (mine === epoch) data.value = value
  } catch {
    if (mine === epoch) error.value = true
  } finally {
    if (mine === epoch) busy.value = false
  }
}
async function mutate(payload: Record<string, unknown>) {
  if (
    !data.value ||
    busy.value ||
    error.value ||
    !authenticated.value ||
    !window.confirm(t('ilConfirm'))
  )
    return
  const mine = epoch
  busy.value = true
  error.value = false
  try {
    const r = await authenticatedFetch(new URL('/api/memory-identities', location.origin), {
      method: 'POST',
      signal: controller?.signal,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        ...payload,
        expectedRevision: data.value.revision,
        operationId: crypto.randomUUID(),
        confirmed: true,
      }),
    })
    if (!r.ok) throw new Error('mutation')
    if (mine === epoch) await load()
  } catch {
    if (mine === epoch) error.value = true
  } finally {
    if (mine === epoch) busy.value = false
  }
}
function putAlias() {
  const scene = scenes.value.find((s) => s.id === sceneId.value)
  if (!scene) return
  const date = (value: string) => (value ? new Date(value).toISOString() : null)
  try {
    void mutate({
      action: 'alias_put',
      aliasId: crypto.randomUUID(),
      sceneId: scene.id,
      endpoint: { adapterId: scene.adapterId, accountId: scene.accountId, personId: left.personId },
      label: aliasLabel.value,
      validFrom: date(validFrom.value),
      validUntil: date(validUntil.value),
    })
  } catch {
    error.value = true
  }
}
watch([expanded, authenticated], () => {
  if (!expanded.value || !authenticated.value) {
    Object.assign(left, { adapterId: '', accountId: '', personId: '' })
    Object.assign(right, { adapterId: '', accountId: '', personId: '' })
    sceneId.value = aliasLabel.value = validFrom.value = validUntil.value = ''
  }
  void load()
})
onBeforeUnmount(clear)
</script>
<template>
  <section class="mt-6 rounded-lg border border-default p-4">
    <UButton variant="ghost" @click="expanded = !expanded">{{ t('ilTitle') }}</UButton>
    <div v-if="expanded" class="mt-3 space-y-4">
      <p class="text-sm text-muted">{{ t('ilHelp') }}</p>
      <p v-if="busy" role="status">{{ t('ilLoading') }}</p>
      <p v-if="error" role="alert">{{ t('ilError') }}</p>
      <UButton :disabled="busy" @click="load">{{ t('ilRefresh') }}</UButton>
      <template v-if="data">
        <p>{{ t('ilRevision') }}: {{ data.revision }}</p>
        <div class="grid gap-3 sm:grid-cols-2">
          <fieldset
            v-for="(endpoint, index) in [left, right]"
            :key="index"
            class="space-y-2 rounded border border-default p-3"
          >
            <legend>{{ index ? t('ilRight') : t('ilLeft') }}</legend>
            <label class="block"
              >{{ t('ilAdapter')
              }}<UInput v-model="endpoint.adapterId" class="w-full" :disabled="busy"
            /></label>
            <label class="block"
              >{{ t('ilAccount')
              }}<UInput v-model="endpoint.accountId" class="w-full" :disabled="busy"
            /></label>
            <label class="block"
              >{{ t('ilPerson')
              }}<UInput v-model="endpoint.personId" class="w-full" :disabled="busy"
            /></label>
          </fieldset>
        </div>
        <div class="flex flex-wrap gap-2">
          <UButton
            :disabled="busy || error || !left.personId || !right.personId"
            @click="mutate({ action: 'link', left, right })"
            >{{ t('ilLink') }}</UButton
          >
          <UButton
            :disabled="busy || error || !left.personId || !right.personId"
            @click="mutate({ action: 'unlink', left, right })"
            >{{ t('ilUnlink') }}</UButton
          >
          <UButton
            :disabled="busy || error || !left.personId"
            color="warning"
            @click="mutate({ action: 'split', partition: [left] })"
            >{{ t('ilSplit') }}</UButton
          >
        </div>
        <p class="text-sm text-muted">{{ t('ilSplitHelp') }}</p>
        <ul class="space-y-2">
          <li
            v-for="(edge, index) in data.edges"
            :key="index"
            class="break-all rounded bg-elevated p-2"
          >
            {{ endpointLabel(edge[0]) }} ↔ {{ endpointLabel(edge[1]) }}
          </li>
        </ul>
        <p v-if="!data.edges.length">{{ t('ilNoLinks') }}</p>
        <h4 class="font-medium">{{ t('ilAliases') }}</h4>
        <p class="text-sm text-muted">{{ t('ilAliasHelp') }}</p>
        <label class="block"
          >{{ t('ilScene')
          }}<select
            v-model="sceneId"
            :disabled="busy"
            class="ml-2 rounded border border-default bg-default p-2"
          >
            <option value="">—</option>
            <option v-for="scene in scenes" :key="scene.id" :value="scene.id">
              {{ scene.name }}
            </option>
          </select></label
        >
        <label class="block"
          >{{ t('ilAliasLabel') }}<UInput v-model="aliasLabel" :disabled="busy" class="ml-2"
        /></label>
        <div class="flex flex-wrap gap-3">
          <label
            >{{ t('ilFrom')
            }}<input
              v-model="validFrom"
              type="datetime-local"
              :disabled="busy"
              class="block rounded border border-default bg-default p-2" /></label
          ><label
            >{{ t('ilUntil')
            }}<input
              v-model="validUntil"
              type="datetime-local"
              :disabled="busy"
              class="block rounded border border-default bg-default p-2"
          /></label>
        </div>
        <p class="text-sm text-muted">{{ t('ilTimeHelp') }}</p>
        <UButton
          :disabled="busy || error || !sceneId || !left.personId || !aliasLabel.trim()"
          @click="putAlias"
          >{{ t('ilPutAlias') }}</UButton
        >
        <ul class="space-y-2">
          <li
            v-for="alias in data.aliases"
            :key="alias.id"
            class="rounded border border-default p-3"
          >
            <p>{{ alias.label }}</p>
            <p class="break-all text-sm">
              {{ endpointLabel(alias.endpoint) }} · {{ alias.scopeSceneId }}
            </p>
            <p class="text-sm">{{ alias.validFrom || '—' }} → {{ alias.validUntil || '—' }}</p>
            <UButton
              :disabled="busy || error"
              color="warning"
              variant="ghost"
              @click="
                mutate({ action: 'alias_remove', sceneId: alias.scopeSceneId, aliasId: alias.id })
              "
              >{{ t('ilRemove') }}</UButton
            >
          </li>
        </ul>
      </template>
    </div>
  </section>
</template>
