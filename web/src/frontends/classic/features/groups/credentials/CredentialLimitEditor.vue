<script setup lang="ts">
import { PencilLine } from '@lucide/vue'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import AppButton from '@/components/ui/AppButton.vue'
import IconButton from '@/components/ui/IconButton.vue'
import { formatInteger } from '@/lib/format'

const props = defineProps<{
  // 凭据自身值。0 表示继承分组，不是不限。
  value: number
  // 分组下发的默认限额。0 表示分组不限。
  groupLimit: number
  // 解析后实际生效的限额。0 表示不限。
  effectiveLimit: number
  disabled: boolean
  label: string
  inheritedLabel: string
  unlimitedLabel: string
  hint: string
  invalidLabel: string
  save: (value: number) => Promise<void>
}>()
const { locale, t } = useI18n()
const editing = ref(false)
const draft = ref('')
const error = ref(false)

const inherited = computed(() => props.value === 0)
const display = computed(() =>
  props.effectiveLimit === 0
    ? props.unlimitedLabel
    : formatInteger(props.effectiveLimit, locale.value),
)
const parsed = computed(() => {
  const trimmed = draft.value.trim()
  if (trimmed === '') return 0
  const value = Number(trimmed)
  return Number.isSafeInteger(value) && value >= 0 ? value : Number.NaN
})
const valid = computed(() => !Number.isNaN(parsed.value))

watch(
  () => props.value,
  () => {
    if (!editing.value) reset()
  },
)

function reset(): void {
  draft.value = props.value === 0 ? '' : String(props.value)
  error.value = false
}

function beginEdit(): void {
  if (props.disabled) return
  reset()
  editing.value = true
}

function cancel(): void {
  editing.value = false
  reset()
}

async function submit(): Promise<void> {
  if (props.disabled || !valid.value || parsed.value === props.value) {
    if (valid.value) cancel()
    return
  }
  error.value = false
  try {
    await props.save(parsed.value)
    editing.value = false
  } catch {
    error.value = true
  }
}
</script>

<template>
  <div class="setting-panel">
    <span class="setting-panel__title">{{ label }}</span>
    <div class="setting-panel__body">
      <template v-if="!editing">
        <span v-if="inherited" class="setting-panel__tag">{{ inheritedLabel }}</span>
        <span class="setting-panel__value">{{ display }}</span>
        <IconButton
          class="setting-panel__edit"
          variant="ghost"
          tone="action"
          size="xs"
          :label="label"
          :disabled="disabled"
          @click="beginEdit"
        >
          <PencilLine :size="12" aria-hidden="true" />
        </IconButton>
      </template>
      <form v-else class="setting-panel__form" @submit.prevent="submit">
        <label class="sr-only">{{ label }}</label>
        <input
          v-model="draft"
          class="credential-limit-editor__input"
          type="number"
          min="0"
          step="1"
          inputmode="numeric"
          :placeholder="groupLimit === 0 ? unlimitedLabel : String(groupLimit)"
          :disabled="disabled"
          :aria-invalid="!valid || undefined"
        />
        <div class="setting-panel__actions">
          <AppButton variant="ghost" size="compact" @click="cancel">
            {{ t('group.credentials.weightEditor.cancel') }}
          </AppButton>
          <AppButton type="submit" size="compact" :disabled="disabled || !valid">
            {{ t('group.credentials.weightEditor.save') }}
          </AppButton>
        </div>
        <p v-if="!valid" class="setting-panel__error" role="alert">{{ invalidLabel }}</p>
        <p v-else-if="error" class="setting-panel__error" role="alert">
          {{ t('group.credentials.updateFailed') }}
        </p>
        <p v-else class="credential-limit-editor__hint">{{ hint }}</p>
      </form>
    </div>
  </div>
</template>

<style scoped>
.credential-limit-editor__input {
  width: 88px;
  min-height: 26px;
  flex: none;
  border: 1px solid var(--color-border-control);
  border-radius: var(--radius-control);
  background: var(--color-surface);
  color: var(--color-text);
  padding: 0 6px;
  font-family: var(--font-mono);
  font-size: var(--text-label-xs);
}

.credential-limit-editor__hint {
  flex: 1 1 100%;
  margin: 0;
  color: var(--color-text-faint);
  font-size: var(--text-label-xs);
}
</style>
