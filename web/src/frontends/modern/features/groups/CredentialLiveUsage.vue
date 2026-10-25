<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CredentialRow } from '@modern/api/group-detail'
import { AppProgressBar, AppTooltip, type SemanticTone } from '@modern/components/ui'

const props = defineProps<{ row: CredentialRow; inline?: boolean }>()
const { t, n } = useI18n()

// 上限为 0 时限流器不记账，已用数恒为 0。这时只显示“不限”，避免写出 0/∞。
function unlimited(limit: number) {
  return limit <= 0
}
function remaining(used: number, limit: number) {
  if (unlimited(limit)) return 100
  return Math.max(0, Math.min(100, ((limit - used) / limit) * 100))
}
function tone(used: number, limit: number): SemanticTone {
  if (unlimited(limit)) return 'info'
  const left = remaining(used, limit)
  if (used >= limit || left < 30) return 'danger'
  if (left < 70) return 'warning'
  return 'success'
}
function value(used: number, limit: number) {
  return unlimited(limit)
    ? '∞'
    : t('credentialCards.liveUsage.summary', { used: n(used), limit: n(limit) })
}
function describeMeter(key: string, label: string, helpText: string, used: number, limit: number) {
  const display = value(used, limit)
  return {
    key,
    label,
    display,
    help: `${label} ${display}\n${helpText}`,
    tone: tone(used, limit),
    remaining: remaining(used, limit),
  }
}
const meters = computed(() => [
  describeMeter(
    'rpm',
    t('credentialCards.liveUsage.rpm'),
    t('credentialCards.liveUsage.rpmHelp'),
    props.row.rpmUsed,
    props.row.effectiveRpmLimit,
  ),
  describeMeter(
    'concurrency',
    t('credentialCards.liveUsage.concurrency'),
    t('credentialCards.liveUsage.concurrencyHelp'),
    props.row.concurrencyUsed,
    props.row.effectiveConcurrencyLimit,
  ),
])
</script>

<template>
  <div class="modern-credential-live-usage" :class="{ 'is-inline': inline }">
    <AppTooltip v-for="meter in meters" :key="meter.key" :label="meter.help">
      <span class="modern-credential-live-usage-meter" tabindex="0">
        <span class="modern-credential-live-usage-line">
          <span class="modern-credential-live-usage-label">{{ meter.label }}</span>
          <span class="modern-credential-live-usage-value" :class="`is-${meter.tone}`">{{
            meter.display
          }}</span>
        </span>
        <AppProgressBar
          :value="meter.remaining"
          :tone="meter.tone"
          :label="`${meter.label} ${meter.display}`"
          size="sm"
        />
      </span>
    </AppTooltip>
  </div>
</template>

<style scoped>
.modern-credential-live-usage {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--modern-space-3);
  min-width: 0;
}
.modern-credential-live-usage-meter {
  display: grid;
  gap: var(--modern-space-0-5);
  min-width: 0;
}
.modern-credential-live-usage-line {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--modern-space-1);
  min-width: 0;
}
.modern-credential-live-usage-label {
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
}
.modern-credential-live-usage-value {
  color: var(--modern-text);
  font-size: var(--modern-font-size-small);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.modern-credential-live-usage-value.is-warning {
  color: var(--modern-warning);
}
.modern-credential-live-usage-value.is-danger {
  color: var(--modern-danger);
}
.modern-credential-live-usage-value.is-info {
  color: var(--modern-info);
}
.modern-credential-live-usage.is-inline {
  width: max-content;
  max-width: 100%;
  grid-template-columns: repeat(2, max-content);
}
.modern-credential-live-usage.is-inline .modern-credential-live-usage-meter {
  min-width: calc(var(--modern-space-10) * 2);
}
</style>
