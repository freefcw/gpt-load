<script setup lang="ts">
import { AppFormSection, AppTextField } from '@modern/components/ui'
import { useI18n } from 'vue-i18n'

const rpm = defineModel<string>('rpm', { required: true })
const concurrency = defineModel<string>('concurrency', { required: true })

defineProps<{
  title: string
  hint?: string
  disabled?: boolean
  rpmPlaceholder?: string
  concurrencyPlaceholder?: string
  rpmError?: string
  concurrencyError?: string
  summary?: string
}>()

const { t } = useI18n()
</script>

<template>
  <AppFormSection compact :title="title" :hint="hint">
    <div class="modern-credential-limit-fields">
      <AppTextField
        v-model="rpm"
        :label="t('credentialCards.limits.rpm')"
        :placeholder="rpmPlaceholder"
        size="sm"
        inputmode="numeric"
        :disabled="disabled"
        :error="rpmError"
      />
      <AppTextField
        v-model="concurrency"
        :label="t('credentialCards.limits.concurrency')"
        :placeholder="concurrencyPlaceholder"
        size="sm"
        inputmode="numeric"
        :disabled="disabled"
        :error="concurrencyError"
      />
    </div>
    <p v-if="summary" class="modern-credential-limit-summary">{{ summary }}</p>
  </AppFormSection>
</template>

<style scoped>
.modern-credential-limit-fields {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: var(--modern-space-3);
}

.modern-credential-limit-summary {
  margin: 0;
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
}
</style>
