<script setup lang="ts">
import { onBeforeUnmount, onDeactivated, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { adminApi, errorMessage } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import { legacyVideoPricingTarget } from '@/components/pricing/legacyVideoPricing'
import type { PricingCatalog } from '@/components/pricing/types'

const route = useRoute(), router = useRouter(), auth = useAuth(), { t } = useI18n()
const loading = ref(false), error = ref('')
let generation = 0
async function redirect() {
  const current = ++generation
  error.value = ''; loading.value = false
  if (route.path !== '/app/video-pricing') return
  if (!auth.isSuperadmin) { error.value = t('pricingx.superadmin'); return }
  loading.value = true
  const query = { ...route.query }
  try {
    const catalog = await adminApi<PricingCatalog>('GET', '/api/admin/pricing-catalog')
    if (current !== generation) return
    await router.replace(legacyVideoPricingTarget(query, catalog.models))
  } catch (e) { if (current === generation) error.value = errorMessage(e, t('pricingx.loadFailed')) }
  finally { if (current === generation) loading.value = false }
}
watch(() => [route.fullPath, auth.orgId, auth.token, auth.role], () => { void redirect() }, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { ++generation })
onDeactivated(() => { ++generation; error.value = ''; loading.value = false })
</script>
<template>
  <section class="legacy-video-pricing">
    <p v-if="loading" role="status">{{ t('common.loading') }}</p>
    <template v-else-if="error"><p role="alert">{{ error }}</p><button type="button" class="btn-ghost" :disabled="!auth.isSuperadmin" @click="redirect">{{ t('common.retry') }}</button></template>
  </section>
</template>
<style scoped>
.legacy-video-pricing{padding:24px;color:rgb(var(--c-muted))}.legacy-video-pricing p{margin-bottom:16px}
</style>
