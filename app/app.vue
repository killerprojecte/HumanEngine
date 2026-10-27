<script setup lang="ts">
const { t, locale, setLocale } = useI18n()
const { authenticated, authReady, authMessage, restoreSession, loginSession, logoutSession } =
  useApi()
const route = useRoute()
const sidebarPreference = useCookie<boolean | null>('humanengine-sidebar-collapsed', {
  default: () => null,
  sameSite: 'lax',
})
const collapsed = ref(sidebarPreference.value ?? false)
function setCollapsed(value: boolean) {
  collapsed.value = value
  sidebarPreference.value = value
}
const logoutFailed = ref(false)
const secret = ref(''),
  error = ref(''),
  busy = ref(false)
const links = computed(() =>
  [
    { label: t('overview'), icon: 'i-lucide-layout-dashboard', to: '/' },
    { label: t('models'), icon: 'i-lucide-brain', to: '/models' },
    { label: t('chat'), icon: 'i-lucide-message-circle', to: '/chat' },
    { label: t('memories'), icon: 'i-lucide-notebook-pen', to: '/memories' },
    { label: t('rules'), icon: 'i-lucide-book-open', to: '/rules' },
    { label: t('memes'), icon: 'i-lucide-images', to: '/memes' },
    { label: t('plugins'), icon: 'i-lucide-puzzle', to: '/plugins' },
    { label: t('scenes'), icon: 'i-lucide-messages-square', to: '/scenes' },
    { label: t('settings'), icon: 'i-lucide-sliders-horizontal', to: '/settings' },
    { label: t('logs'), icon: 'i-lucide-scroll-text', to: '/logs' },
  ].map((item) => ({ ...item, 'aria-label': item.label })),
)
let authChannel: BroadcastChannel | undefined
function notifyAuthChange() {
  authChannel?.postMessage('changed')
}
async function login() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    if (await loginSession(secret.value)) {
      logoutFailed.value = false
      notifyAuthChange()
    }
  } catch (cause) {
    error.value = t(
      cause instanceof Error && cause.message === 'invalid' ? 'authInvalid' : 'networkError',
    )
  } finally {
    secret.value = ''
    busy.value = false
  }
}
async function logout() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    await logoutSession()
    logoutFailed.value = false
    notifyAuthChange()
  } catch {
    logoutFailed.value = true
    error.value = t('authLogoutFailed')
  } finally {
    busy.value = false
  }
}
function revalidate() {
  if (!busy.value && !logoutFailed.value) void restoreSession()
}
function onVisibility() {
  if (document.visibilityState === 'visible') revalidate()
}
onMounted(() => {
  if ('BroadcastChannel' in window) {
    authChannel = new BroadcastChannel('humanengine-auth')
    authChannel.onmessage = revalidate
  }
  window.addEventListener('pageshow', revalidate)
  window.addEventListener('focus', revalidate)
  document.addEventListener('visibilitychange', onVisibility)
  void restoreSession()
})
onBeforeUnmount(() => {
  authChannel?.close()
  window.removeEventListener('pageshow', revalidate)
  window.removeEventListener('focus', revalidate)
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>
<template>
  <UApp>
    <div
      v-if="!authenticated || !authReady || busy"
      class="min-h-screen grid place-items-center p-6"
    >
      <UCard class="w-full max-w-md">
        <template #header
          ><div class="flex items-center gap-3">
            <UIcon name="i-lucide-sparkles" class="text-primary text-3xl" />
            <div>
              <h1 class="text-xl font-semibold">HumanEngine</h1>
              <p class="text-muted text-sm">{{ t('tagline') }}</p>
            </div>
          </div></template
        >
        <p v-if="!authReady" role="status">{{ t('authChecking') }}</p>
        <form v-else class="space-y-5" @submit.prevent="login">
          <UButton v-if="logoutFailed" :loading="busy" @click="logout">{{
            t('authRetryLogout')
          }}</UButton>
          <p class="text-sm text-muted">{{ t('loginHelp') }}</p>
          <UAlert
            v-if="authMessage"
            color="warning"
            :title="t(authMessage === 'expired' ? 'authExpired' : 'networkError')"
          />
          <UFormField :label="t('adminToken')"
            ><UInput
              v-model="secret"
              type="password"
              autocomplete="current-password"
              class="w-full"
              required /></UFormField
          ><UAlert v-if="error" color="error" :title="error" /><UButton
            type="submit"
            block
            :loading="busy"
            >{{ t('connect') }}</UButton
          >
        </form>
      </UCard>
    </div>
    <UDashboardGroup v-else unit="rem" :persistent="false">
      <UDashboardSidebar
        :collapsed="collapsed"
        @update:collapsed="setCollapsed"
        collapsible
        :default-size="11.5"
        :collapsed-size="4"
        :min-size="11.5"
        :max-size="11.5"
        :ui="{ body: 'px-2 py-3 gap-5', header: 'px-4', footer: 'px-4 py-4' }"
      >
        <template #header="{ collapsed: compact }">
          <NuxtLink
            to="/"
            :aria-label="t('overview')"
            class="flex items-center gap-2.5 min-w-0 font-semibold text-highlighted"
          >
            <span
              class="size-8 shrink-0 grid place-items-center rounded-lg bg-primary text-inverted"
              ><UIcon name="i-lucide-sparkles" class="size-[18px]"
            /></span>
            <span v-if="!compact" class="tracking-tight">HumanEngine</span>
          </NuxtLink>
        </template>
        <template #default="{ collapsed: compact }">
          <div
            v-for="(group, index) in [links.slice(0, 3), links.slice(3, 6), links.slice(6)]"
            :key="index"
          >
            <p
              v-if="!compact"
              class="px-3 mb-2 text-[11px] font-semibold uppercase tracking-wider text-dimmed"
            >
              {{ t(['workspace', 'uxKnowledge', 'uxSystem'][index]!) }}
            </p>
            <UNavigationMenu
              :items="group"
              orientation="vertical"
              :collapsed="compact"
              tooltip
              class="w-full"
            />
          </div>
        </template>
        <template #footer="{ collapsed: compact }">
          <span v-if="!compact" class="text-xs text-muted flex items-center gap-2"
            ><UIcon name="i-lucide-user-round" class="size-4" />{{ t('singleRole') }}</span
          >
        </template>
      </UDashboardSidebar>
      <UDashboardPanel
        :ui="{
          body:
            route.path === '/chat'
              ? 'p-0 sm:p-0 gap-0 overflow-hidden'
              : [
                    '/',
                    '/logs',
                    '/models',
                    '/scenes',
                    '/memories',
                    '/rules',
                    '/settings',
                    '/memes',
                    '/plugins',
                  ].includes(route.path)
                ? 'p-4 sm:p-6 gap-4'
                : 'p-4 sm:p-5 gap-4',
        }"
      >
        <template #header>
          <UDashboardNavbar :title="t('workspace')" :toggle="false">
            <template #leading
              ><UDashboardSidebarToggle class="lg:hidden" /><UDashboardSidebarCollapse
                class="hidden lg:flex"
            /></template>
            <template #right>
              <USelect
                :model-value="locale"
                :items="[
                  { label: 'English', value: 'en' },
                  { label: t('localeSimplified'), value: 'zh-CN' },
                  { label: t('localeTraditional'), value: 'zh-TW' },
                ]"
                :aria-label="t('uxLanguage')"
                class="w-32"
                size="sm"
                variant="ghost"
                @update:model-value="setLocale($event as 'en' | 'zh-CN' | 'zh-TW')"
              />
              <UColorModeButton size="sm" />
              <UTooltip :text="t('disconnect')"
                ><UButton
                  color="neutral"
                  variant="ghost"
                  icon="i-lucide-log-out"
                  :aria-label="t('disconnect')"
                  @click="logout"
              /></UTooltip>
            </template>
          </UDashboardNavbar>
        </template>
        <template #body><NuxtPage /></template>
      </UDashboardPanel>
    </UDashboardGroup>
  </UApp>
</template>
