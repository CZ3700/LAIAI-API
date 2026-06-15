<script setup lang="ts">
const gradientId = `laiai-mark-${Math.random().toString(36).slice(2)}`

withDefaults(defineProps<{
  size?: 'sm' | 'md' | 'lg'
  showWord?: boolean
  subtitle?: string
  wordClass?: string
}>(), {
  size: 'md',
  showWord: true,
  subtitle: '',
  wordClass: '',
})
</script>

<template>
  <span class="brand-mark" :class="[`brand-mark-${size}`, { 'brand-mark-lockup': showWord }]">
    <span class="brand-symbol" aria-hidden="true">
      <svg viewBox="0 0 48 48" role="img" focusable="false">
        <defs>
          <linearGradient :id="gradientId" x1="7" y1="41" x2="41" y2="7" gradientUnits="userSpaceOnUse">
            <stop offset="0" stop-color="rgb(var(--c-accent-2))" />
            <stop offset="0.48" stop-color="rgb(var(--c-accent))" />
            <stop offset="1" stop-color="rgb(var(--c-ok))" />
          </linearGradient>
        </defs>
        <path class="brand-symbol-plane" d="M24 5.5 40.5 15v18L24 42.5 7.5 33V15L24 5.5Z" />
        <path class="brand-symbol-route" :stroke="`url(#${gradientId})`" d="M8.5 16.5c7.7 0 9.2 6.7 15.5 7.5 6.3.8 7.8 7.5 15.5 7.5" />
        <path class="brand-symbol-route brand-symbol-route-b" :stroke="`url(#${gradientId})`" d="M8.5 31.5c7.7 0 9.2-6.7 15.5-7.5 6.3-.8 7.8-7.5 15.5-7.5" />
        <path class="brand-symbol-flow" :fill="`url(#${gradientId})`" d="M24 11.5 35.5 36h-6.4l-1.55-4.05h-7.1L18.9 36h-6.4L24 11.5Zm-1.7 15.45h3.4L24 20.8l-1.7 6.15Z" />
        <circle class="brand-symbol-node" cx="8.5" cy="16.5" r="2" />
        <circle class="brand-symbol-node" cx="8.5" cy="31.5" r="2" />
        <circle class="brand-symbol-node" cx="39.5" cy="16.5" r="2" />
        <circle class="brand-symbol-node" cx="39.5" cy="31.5" r="2" />
      </svg>
    </span>
    <span v-if="showWord" class="brand-word-wrap">
      <span class="brand-word" :class="wordClass">LAIAI</span>
      <span v-if="subtitle" class="brand-subtitle">{{ subtitle }}</span>
    </span>
  </span>
</template>
