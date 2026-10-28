<script setup lang="ts">
import { avatarSource, identityColor } from '~/utils/chatIdentity'
const props = withDefaults(
  defineProps<{ name: string; identity: string; src?: string; group?: boolean; large?: boolean }>(),
  { src: undefined },
)
const failed = ref(false)
const source = computed(() => avatarSource(props.src))
watch(source, () => {
  failed.value = false
})
</script>
<template>
  <span
    class="chat-avatar"
    :class="{ 'chat-avatar-large': large }"
    :style="{ backgroundColor: identityColor(identity) }"
    aria-hidden="true"
  >
    <img
      v-if="source && !failed"
      :src="source"
      alt=""
      referrerpolicy="no-referrer"
      loading="lazy"
      @error="failed = true"
    />
    <UIcon v-else-if="group" name="i-lucide-users-round" class="size-5" />
    <span v-else>{{ Array.from(name)[0] || '?' }}</span>
  </span>
</template>
<style scoped>
.chat-avatar {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 38px;
  height: 38px;
  border-radius: 13px;
  color: white;
  font-size: 15px;
  font-weight: 600;
  overflow: hidden;
}
.chat-avatar-large {
  width: 44px;
  height: 44px;
  border-radius: 15px;
}
.chat-avatar img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}
</style>
