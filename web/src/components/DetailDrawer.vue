<script lang="ts">
// Shared across instances so closing a nested drawer cannot unlock the page below it.
const drawerStack: symbol[] = []
let savedOverflow = ''
</script>

<script setup lang="ts">
import { nextTick, onActivated, onDeactivated, onBeforeUnmount, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const props = withDefaults(defineProps<{
  open: boolean
  title: string
  size?: 'md' | 'lg'
  description?: string
  closeLabel?: string
  closeDisabled?: boolean
}>(), { size: 'md', description: '', closeLabel: '', closeDisabled: false })
const emit = defineEmits<{ 'update:open': [open: boolean] }>()
const { t } = useI18n()
const titleId = `detail-drawer-${useId()}`
const panel = ref<HTMLElement | null>(null)
const present = ref(true)
const token = Symbol('detail-drawer')
let active = false
let generation = 0
let returnFocus: HTMLElement | null = null
const isTop = () => drawerStack.at(-1) === token

function close() {
  if (isTop() && !props.closeDisabled) emit('update:open', false)
}
function focusable() {
  return [...(panel.value?.querySelectorAll<HTMLElement>('a[href],button:not(:disabled),input:not(:disabled),select:not(:disabled),textarea:not(:disabled),[tabindex]:not([tabindex="-1"]),audio[controls],video[controls]') ?? [])]
    .filter(el => el.tabIndex >= 0 && el.getClientRects().length > 0 && !el.closest('[inert],[aria-hidden="true"]'))
}
function keydown(event: KeyboardEvent) {
  if (!isTop()) return
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    close()
  } else if (event.key === 'Tab') {
    const items = focusable()
    const first = items[0], last = items.at(-1)
    if (!first) { event.preventDefault(); panel.value?.focus(); return }
    if (event.shiftKey && (document.activeElement === first || document.activeElement === panel.value || !panel.value?.contains(document.activeElement))) {
      event.preventDefault(); last?.focus()
    } else if (!event.shiftKey && (document.activeElement === last || !panel.value?.contains(document.activeElement))) {
      event.preventDefault(); first.focus()
    }
  }
}
function containFocus(event: FocusEvent) {
  if (isTop() && panel.value && event.target instanceof Node && !panel.value.contains(event.target)) panel.value.focus()
}
function release() {
  generation++
  if (!active) return
  const wasTop = isTop()
  active = false
  const index = drawerStack.indexOf(token)
  if (index >= 0) drawerStack.splice(index, 1)
  document.removeEventListener('keydown', keydown, true)
  document.removeEventListener('focusin', containFocus)
  if (!drawerStack.length) document.body.style.overflow = savedOverflow
  const target = returnFocus
  returnFocus = null
  if (wasTop && target?.isConnected) target.focus({ preventScroll: true })
}
watch(() => props.open && present.value, async open => {
  if (!open) { release(); return }
  if (active) return
  active = true
  const current = ++generation
  returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  if (!drawerStack.length) { savedOverflow = document.body.style.overflow; document.body.style.overflow = 'hidden' }
  drawerStack.push(token)
  document.addEventListener('keydown', keydown, true)
  document.addEventListener('focusin', containFocus)
  await nextTick()
  if (current === generation && props.open && isTop()) panel.value?.focus({ preventScroll: true })
}, { immediate: true, flush: 'post' })
onDeactivated(() => { present.value = false; release(); if (props.open) emit('update:open', false) })
onActivated(() => { present.value = true })
onBeforeUnmount(release)
</script>

<template>
  <Teleport to="body">
    <div v-if="open && present" class="detail-drawer-overlay" @click.self="close">
      <section ref="panel" class="detail-drawer-panel" :class="`detail-drawer-${size}`" role="dialog" aria-modal="true" :aria-labelledby="titleId" :aria-describedby="description ? `${titleId}-description` : undefined" tabindex="-1">
        <header class="detail-drawer-header">
          <div class="min-w-0">
            <h2 :id="titleId">{{ title }}</h2>
            <p v-if="description" :id="`${titleId}-description`">{{ description }}</p>
          </div>
          <button type="button" class="btn-ghost shrink-0" :disabled="closeDisabled" :aria-label="closeLabel || t('common.close')" @click="close">{{ closeLabel || t('common.close') }}</button>
        </header>
        <div class="detail-drawer-content"><slot /></div>
        <footer v-if="$slots.footer" class="detail-drawer-footer"><slot name="footer" /></footer>
      </section>
    </div>
  </Teleport>
</template>

<style scoped>
.detail-drawer-overlay{position:fixed;inset:0;z-index:1100;display:flex;justify-content:flex-end;background:rgb(0 0 0 / .48)}
.detail-drawer-panel{width:min(40rem,100vw);height:100%;height:100dvh;display:flex;flex-direction:column;min-width:0;color:rgb(var(--c-ink));background:rgb(var(--c-panel));border-inline-start:1px solid rgb(var(--c-line));box-shadow:-16px 0 48px rgb(15 23 42 / .18);outline:none;animation:detail-drawer-enter .18s ease-out}
.detail-drawer-lg{width:min(60rem,100vw)}
.detail-drawer-header,.detail-drawer-footer{flex:none;padding:1rem 1.25rem;background:rgb(var(--c-panel))}
.detail-drawer-header{display:flex;align-items:flex-start;justify-content:space-between;gap:1rem;border-bottom:1px solid rgb(var(--c-line))}
.detail-drawer-header h2{margin:0;font-size:1rem;font-weight:600;overflow-wrap:anywhere}
.detail-drawer-header p{margin:.5rem 0 0;color:rgb(var(--c-muted));font-size:.8rem;line-height:1.6}
.detail-drawer-content{flex:1;min-height:0;min-width:0;overflow:auto;overscroll-behavior:contain;padding:1.25rem;overflow-wrap:anywhere}
.detail-drawer-footer{border-top:1px solid rgb(var(--c-line))}
@keyframes detail-drawer-enter{from{transform:translateX(24px);opacity:.7}to{transform:translateX(0);opacity:1}}
@media(max-width:640px){.detail-drawer-header,.detail-drawer-footer,.detail-drawer-content{padding:1rem}}
@media(prefers-reduced-motion:reduce){.detail-drawer-panel{animation:none}}
</style>
