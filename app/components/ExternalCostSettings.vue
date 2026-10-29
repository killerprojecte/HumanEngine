<script setup lang="ts">
const { t, locale } = useI18n()
const { authenticatedFetch, authenticated } = useApi()
type Input = {
  id: string
  category: 'tool' | 'storage' | 'compute'
  currency: 'USD'
  billingDate: string
  periodStart: string
  periodEnd: string
  sourceId: string
  status: 'known' | 'unknown'
  amountNanoUsd?: number
}
type Receipt = { revision: number; receipt: Input; recordedAt: string; correctionId: string | null }
type Page = { items: Receipt[]; total: number; page: number; pageSize: number }
const today = () => new Date().toISOString().slice(0, 10)
const blank = (): Input => ({
  id: '',
  category: 'tool',
  currency: 'USD',
  billingDate: today(),
  periodStart: today(),
  periodEnd: today(),
  sourceId: '',
  status: 'unknown',
})
const form = ref<Input>(blank()),
  page = ref<Page>(),
  pageNumber = ref(1),
  revision = ref<number>(),
  correctionId = ref(''),
  busy = ref(false),
  error = ref(false),
  uncertain = ref(false),
  saved = ref(false)
let generation = 0,
  controller: AbortController | undefined,
  disposed = false
function clear() {
  ++generation
  controller?.abort()
  page.value = undefined
  busy.value = false
  error.value = false
  saved.value = false
}
function fresh() {
  form.value = blank()
  revision.value = undefined
  correctionId.value = ''
  saved.value = false
  uncertain.value = false
}
function edit(row: Receipt) {
  form.value = { ...row.receipt }
  revision.value = row.revision
  correctionId.value = ''
  saved.value = false
  uncertain.value = false
}
async function refresh() {
  clear()
  if (disposed || !authenticated.value) return
  const epoch = generation
  controller = new AbortController()
  busy.value = true
  try {
    const r = await authenticatedFetch(
      `/api/budget/external-costs?page=${pageNumber.value}&pageSize=20`,
      { signal: controller.signal },
    )
    if (!r.ok) throw Error()
    const value = (await r.json()) as Page
    if (!disposed && epoch === generation && authenticated.value) page.value = value
  } catch {
    if (epoch === generation) error.value = true
  } finally {
    if (epoch === generation) busy.value = false
  }
}
async function submit() {
  if (busy.value || !authenticated.value || !confirm(t('ecConfirm'))) return
  const epoch = ++generation
  controller?.abort()
  controller = new AbortController()
  busy.value = true
  error.value = false
  uncertain.value = false
  saved.value = false
  const receipt: Input = { ...form.value }
  if (receipt.status === 'unknown') delete receipt.amountNanoUsd
  try {
    const r = await authenticatedFetch(
      revision.value ? '/api/budget/external-costs/corrections' : '/api/budget/external-costs',
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        signal: controller.signal,
        body: JSON.stringify(
          revision.value
            ? {
                id: receipt.id,
                expectedRevision: revision.value,
                replacement: receipt,
                correctionId: correctionId.value,
              }
            : receipt,
        ),
      },
    )
    if (!r.ok) {
      if (epoch === generation) uncertain.value = r.status >= 500
      throw Error()
    }
    const row = (await r.json()) as Receipt
    if (disposed || epoch !== generation || !authenticated.value) return
    edit(row)
    await refresh()
    saved.value = true
  } catch {
    if (epoch === generation) {
      error.value = true
      uncertain.value = true
    }
  } finally {
    if (epoch === generation) busy.value = false
  }
}
const amount = (r: Receipt) =>
  r.receipt.status === 'unknown'
    ? t('ecUnknown')
    : new Intl.NumberFormat(locale.value, {
        style: 'currency',
        currency: 'USD',
        maximumFractionDigits: 9,
      }).format(r.receipt.amountNanoUsd! / 1e9)
watch(authenticated, () => {
  fresh()
  if (authenticated.value) void refresh()
  else clear()
})
onMounted(() => void refresh())
onBeforeUnmount(() => {
  disposed = true
  clear()
})
function changePage(delta: number) {
  pageNumber.value += delta
  void refresh()
}
</script>
<template>
  <section class="space-y-3 rounded border border-default p-3" :aria-label="t('ecTitle')">
    <h3 class="font-semibold">{{ t('ecTitle') }}</h3>
    <p class="text-sm text-muted">{{ t('ecHelp') }}</p>
    <p class="text-sm text-warning">{{ t('ecUnknownHelp') }}</p>
    <div class="flex gap-2">
      <UButton :disabled="busy || !authenticated" @click="refresh">{{ t('ecRefresh') }}</UButton
      ><UButton :disabled="busy || !authenticated" @click="fresh">{{ t('ecNew') }}</UButton>
    </div>
    <p v-if="error" role="alert">{{ uncertain ? t('ecUncertain') : t('ecError') }}</p>
    <p v-if="saved" role="status">{{ t('ecSaved') }}</p>
    <ul v-if="page" class="space-y-2">
      <li v-for="row in page.items" :key="row.receipt.id" class="rounded border p-2 text-sm">
        <p>
          {{ row.receipt.id }} · v{{ row.revision }} ·
          {{ t('ecCategory_' + row.receipt.category) }} · {{ amount(row) }}
        </p>
        <p>
          {{ t('ecBillingDate') }}: {{ row.receipt.billingDate }} · {{ t('ecPeriod') }}:
          {{ row.receipt.periodStart }} – {{ row.receipt.periodEnd }}
        </p>
        <p>
          {{ t('ecSource') }}: {{ row.receipt.sourceId }} · {{ t('ecRecorded') }}:
          {{ row.recordedAt }}
        </p>
        <UButton :disabled="busy || !authenticated" @click="edit(row)">{{
          t('ecCorrect')
        }}</UButton>
      </li>
    </ul>
    <div v-if="page" class="flex items-center gap-2">
      <UButton :disabled="busy || pageNumber <= 1" @click="changePage(-1)">{{
        t('ecPrevious')
      }}</UButton
      ><span>{{ pageNumber }} · {{ page.total }}</span
      ><UButton :disabled="busy || pageNumber * 20 >= page.total" @click="changePage(1)">{{
        t('ecNext')
      }}</UButton>
    </div>
    <form class="grid gap-2 md:grid-cols-2" @submit.prevent="submit">
      <label
        >{{ t('ecId')
        }}<input
          v-model="form.id"
          required
          pattern="[A-Za-z0-9_-]{1,160}"
          maxlength="160"
          :disabled="busy || !authenticated || revision !== undefined"
          class="block w-full border rounded p-2"
      /></label>
      <label
        >{{ t('ecSource')
        }}<input
          v-model="form.sourceId"
          required
          pattern="[A-Za-z0-9_-]{1,160}"
          maxlength="160"
          :disabled="busy || !authenticated"
          class="block w-full border rounded p-2"
      /></label>
      <label
        >{{ t('ecCategory')
        }}<select
          v-model="form.category"
          :disabled="busy || !authenticated"
          class="block w-full border rounded p-2"
        >
          <option value="tool">{{ t('ecCategory_tool') }}</option>
          <option value="storage">{{ t('ecCategory_storage') }}</option>
          <option value="compute">{{ t('ecCategory_compute') }}</option>
        </select></label
      >
      <label
        >{{ t('ecStatus')
        }}<select
          v-model="form.status"
          :disabled="busy || !authenticated"
          class="block w-full border rounded p-2"
        >
          <option value="unknown">{{ t('ecUnknown') }}</option>
          <option value="known">{{ t('ecKnown') }}</option>
        </select></label
      >
      <label
        >{{ t('ecBillingDate')
        }}<input
          v-model="form.billingDate"
          type="date"
          required
          :disabled="busy || !authenticated"
          class="block w-full border rounded p-2"
      /></label>
      <label v-if="form.status === 'known'"
        >{{ t('ecNano')
        }}<input
          v-model.number="form.amountNanoUsd"
          type="number"
          min="0"
          max="9007199254740991"
          step="1"
          required
          :disabled="busy || !authenticated"
          class="block w-full border rounded p-2"
      /></label>
      <label
        >{{ t('ecStart')
        }}<input
          v-model="form.periodStart"
          type="date"
          required
          :disabled="busy || !authenticated"
          class="block w-full border rounded p-2"
      /></label>
      <label
        >{{ t('ecEnd')
        }}<input
          v-model="form.periodEnd"
          type="date"
          required
          :disabled="busy || !authenticated"
          class="block w-full border rounded p-2"
      /></label>
      <label v-if="revision !== undefined"
        >{{ t('ecCorrectionId') }} · v{{ revision
        }}<input
          v-model="correctionId"
          required
          pattern="[A-Za-z0-9_-]{1,160}"
          maxlength="160"
          :disabled="busy || !authenticated"
          class="block w-full border rounded p-2"
      /></label>
      <div class="md:col-span-2">
        <UButton type="submit" :disabled="busy || !authenticated">{{
          revision === undefined ? t('ecImport') : t('ecCorrect')
        }}</UButton>
      </div>
    </form>
    <p class="text-sm text-muted">{{ t('ecRetentionHelp') }}</p>
  </section>
</template>
