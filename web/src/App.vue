<script setup lang="ts">
import { watch, onMounted, onBeforeUnmount } from 'vue'
import { useAuth } from '@/stores/auth'
import { useCurrency } from '@/stores/currency'
const auth = useAuth(), currency = useCurrency()
const refreshCurrency = () => { if (!document.hidden) void currency.load(auth.token) }
watch(() => auth.token, token => void currency.load(token), { immediate: true, flush: 'sync' })
let refreshTimer: ReturnType<typeof setInterval>
onMounted(() => { window.addEventListener('focus', refreshCurrency); refreshTimer = setInterval(refreshCurrency, 60000) })
onBeforeUnmount(() => { window.removeEventListener('focus', refreshCurrency); clearInterval(refreshTimer) })
</script>

<template>
  <router-view v-slot="{ Component }">
    <transition name="page" mode="out-in">
      <component :is="Component" />
    </transition>
  </router-view>
</template>
