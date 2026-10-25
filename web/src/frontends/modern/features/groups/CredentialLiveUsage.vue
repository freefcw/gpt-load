<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CredentialRow } from '@modern/api/group-detail'
import { describeLiveLimitUsage, type LiveLimitUsageTone } from '@shared/live-limit-usage'
import { AppProgressBar, AppTooltip, type SemanticTone } from '@modern/components/ui'

const props = defineProps<{ row: CredentialRow; inline?: boolean }>()
const { t, n } = useI18n()

const semanticTone: Record<LiveLimitUsageTone, SemanticTone> = {
  unlimited: 'info',
  success: 'success',
  warning: 'warning',
  danger: 'danger',
}

function describeMeter(key: string, label: string, helpText: string, used: number, limit: number) {
  const usage = describeLiveLimitUsage(used, limit)
  const display = usage.unlimited
    ? '∞'
    : t('credentialCards.liveUsage.summary', { used: n(used), limit: n(limit) })
  return {
    key,
    label,
    display,
    help: `${label} ${display}\n${helpText}`,
    tone: semanticTone[usage.tone],
    remaining: usage.remainingPercent,
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
