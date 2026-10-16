<script setup lang="ts">
import { useSeedanceI18n } from './messages'
import type { Metering } from './types'
defineProps<{ value: Metering }>()
const { t } = useSeedanceI18n()
</script>
<template>
  <div class="space-y-2 rounded border border-line p-4 text-sm">
    <h3 class="font-medium">{{ t('seedance.metering') }}</h3>
    <p class="text-xs text-muted">{{ t('seedance.meteringNotice') }}</p>
    <div class="grid gap-2 sm:grid-cols-2">
      <p>{{ t('seedance.inputVideoCount') }}: {{ value.input_video_count ?? t('seedance.unknown') }}</p>
      <p>{{ t('seedance.inputDuration') }}: {{ value.input_duration_ms }} ms · {{ t(`seedance.${value.input_duration_source}`) }}</p>
      <p>{{ t('seedance.outputDuration') }}: {{ value.output_duration_ms }} ms · {{ t('seedance.frames') }}: {{ value.output_frames }}</p>
      <p>{{ t('seedance.dimensions') }}: {{ value.width }} × {{ value.height }} · {{ value.resolution || '—' }} / {{ value.ratio || '—' }}</p>
      <p>FPS: {{ value.fps_numerator }} / {{ value.fps_denominator }}</p>
      <p>{{ t('seedance.minimumTokens') }}: {{ value.minimum_tokens ?? '—' }}</p>
      <p class="break-all">{{ value.mode }} · {{ value.service_tier }} · {{ value.capability_version }}</p>
      <p v-if="value.minimum_rule_version" class="break-all">{{ value.minimum_rule_version }}</p>
    </div>
  </div>
</template>
