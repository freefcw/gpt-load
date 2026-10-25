<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import { describeLiveLimitUsage } from '@shared/live-limit-usage'
import QuotaProgressBar from '@/components/ui/QuotaProgressBar.vue'

// 限额实时用量：已用/上限 + 进度条。上限为 0 时限流器不记账，已用数恒为 0，
// 所以不写“0/∞”，只留 ∞，并用独立颜色表示“没有上限”。
const props = defineProps<{
  label: string
  used: number
  limit: number
  compact?: boolean
}>()
const { n } = useI18n()

const usage = computed(() => describeLiveLimitUsage(props.used, props.limit))
const text = computed(() => (usage.value.unlimited ? '∞' : `${n(props.used)}/${n(props.limit)}`))
</script>

<template>
  <span class="limit-usage" :class="{ 'limit-usage--compact': compact }">
    <span class="limit-usage__label">{{ label }}</span>
    <span class="limit-usage__value" :class="`limit-usage__value--${usage.tone}`">{{ text }}</span>
    <QuotaProgressBar
      class="limit-usage__bar"
      :value="usage.remainingPercent"
      :tone="usage.tone"
      :label="label"
      :value-text="text"
      compact
    />
  </span>
</template>

<style scoped>
.limit-usage {
  display: grid;
  min-width: 0;
  grid-template-columns: auto minmax(0, 1fr);
  align-items: center;
  gap: 2px 6px;
}
.limit-usage__label {
  color: var(--color-text-faint);
  font-size: var(--text-label-xs);
}
.limit-usage__value {
  font-family: var(--font-mono);
  font-size: var(--text-label-xs);
  font-variant-numeric: tabular-nums;
  font-weight: 560;
  text-align: right;
}
.limit-usage__value--warning {
  color: var(--color-warning);
}
.limit-usage__value--danger {
  color: var(--color-danger);
}
.limit-usage__bar {
  grid-column: 1 / -1;
}
.limit-usage--compact {
  gap: 1px 5px;
}
</style>
