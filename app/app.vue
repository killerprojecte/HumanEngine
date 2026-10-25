<script setup lang="ts">
const {t,locale,setLocale}=useI18n()
const {client,token}=useApi()
const secret=ref(''),error=ref(''),busy=ref(false)
const links=computed(()=>[
  {label:t('overview'),icon:'i-lucide-layout-dashboard',to:'/'},
  {label:t('models'),icon:'i-lucide-brain',to:'/models'},
  {label:t('scenes'),icon:'i-lucide-messages-square',to:'/scenes'},
  {label:t('settings'),icon:'i-lucide-sliders-horizontal',to:'/settings'},
  {label:t('logs'),icon:'i-lucide-scroll-text',to:'/logs'},
])
async function login(){busy.value=true;error.value='';token.value=secret.value;try{const result=await client.GET('/api/status');if(result.error){error.value=result.error.error;token.value=''}else secret.value=''}catch{error.value=t('networkError');token.value=''}finally{busy.value=false}}
</script>
<template>
  <UApp>
    <div v-if="!token" class="min-h-screen grid place-items-center p-6">
      <UCard class="w-full max-w-md">
        <template #header><div class="flex items-center gap-3"><UIcon name="i-lucide-sparkles" class="text-primary text-3xl"/><div><h1 class="text-xl font-semibold">HumanEngine</h1><p class="text-muted text-sm">{{t('tagline')}}</p></div></div></template>
        <form class="space-y-5" @submit.prevent="login"><p class="text-sm text-muted">{{t('loginHelp')}}</p><UFormField :label="t('adminToken')"><UInput v-model="secret" type="password" autocomplete="current-password" class="w-full" required/></UFormField><UAlert v-if="error" color="error" :title="error"/><UButton type="submit" block :loading="busy">{{t('connect')}}</UButton></form>
      </UCard>
    </div>
    <UDashboardGroup v-else>
      <UDashboardSidebar collapsible resizable>
        <template #header><NuxtLink to="/" class="flex items-center gap-2 font-semibold text-lg"><UIcon name="i-lucide-sparkles" class="text-primary"/>HumanEngine</NuxtLink></template>
        <UNavigationMenu :items="links" orientation="vertical"/>
        <template #footer><div class="space-y-3 w-full"><USelect :model-value="locale" :items="[{label:'English',value:'en'},{label:t('localeSimplified'),value:'zh-CN'},{label:t('localeTraditional'),value:'zh-TW'}]" class="w-full" @update:model-value="setLocale($event as 'en'|'zh-CN'|'zh-TW')"/><div class="flex justify-between"><UColorModeButton/><UButton variant="ghost" icon="i-lucide-log-out" @click="token=''">{{t('disconnect')}}</UButton></div></div></template>
      </UDashboardSidebar>
      <UDashboardPanel><template #header><UDashboardNavbar :title="t('workspace')"><template #leading><UDashboardSidebarCollapse/></template><template #right><UBadge color="neutral" variant="soft">{{t('singleRole')}}</UBadge></template></UDashboardNavbar></template><template #body><NuxtPage/></template></UDashboardPanel>
    </UDashboardGroup>
  </UApp>
</template>
