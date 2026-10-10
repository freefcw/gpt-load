<script setup lang="ts">
import AppTooltip from './AppTooltip.vue'
import { LoaderCircle, Pencil, Save, X } from '@lucide/vue'
import { PopoverAnchor, PopoverContent, PopoverPortal, PopoverRoot } from 'reka-ui'
import { computed, nextTick, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppFieldControl from './AppFieldControl.vue'
import AppIcon from './AppIcon.vue'
import AppMenuSurface from './AppMenuSurface.vue'
import { overlaySideOffset } from './overlay'

const props = defineProps<{
  modelValue: number
  label: string
  min: number
  max: number
  pending?: boolean
  disabled?: boolean
  error?: string
}>()
const emit = defineEmits<{
  submit: [value: number]
  editing: [value: boolean]
  dirty: [value: boolean]
  clearError: []
}>()
const { t } = useI18n()
const id = useId()
const editing = ref(false)
const draft = ref(String(props.modelValue))
const attempted = ref(false)
const submitted = ref(false)
const input = ref<HTMLInputElement>()
const trigger = ref<HTMLButtonElement>()
const dirty = computed(() => editing.value && draft.value !== String(props.modelValue))
const invalid = computed(
  () =>
    !/^-?\d+$/u.test(draft.value) ||
    Number(draft.value) < props.min ||
    Number(draft.value) > props.max,
)
const error = computed(
  () =>
    props.error ||
    (attempted.value && invalid.value
      ? t('ui.number.range', { min: props.min, max: props.max })
      : ''),
)
watch(dirty, (value) => emit('dirty', value))
watch(editing, (value) => emit('editing', value))
watch(
  () => props.modelValue,
  (value) => {
    if (!editing.value) draft.value = String(value)
  },
)
watch(
  () => props.pending,
  (value) => {
    if (!value && submitted.value) {
      submitted.value = false
      if (!props.error) void cancel()
    }
  },
)
async function start(): Promise<void> {
  if (editing.value || props.disabled || props.pending) return
  editing.value = true
  draft.value = String(props.modelValue)
  attempted.value = false
  emit('clearError')
  await nextTick()
  input.value?.focus({ preventScroll: true })
  input.value?.select()
}
function restore(): void {
  editing.value = false
  draft.value = String(props.modelValue)
  attempted.value = false
  emit('clearError')
}
async function cancel(): Promise<void> {
  if (props.pending) return
  restore()
  await nextTick()
  trigger.value?.focus({ preventScroll: true })
}
function requestClose(open: boolean): void {
  // 仅在没有未保存草稿时才接受外部点击引起的收起。
  if (open || !editing.value || dirty.value) return
  restore()
}
function guardOutside(event: Event): void {
  const target = event.target
  // 有未保存草稿，或点击的就是数字本身时不收起，交由保存/取消处理。
  if (dirty.value || (target instanceof Node && Boolean(trigger.value?.contains(target)))) {
    event.preventDefault()
  }
}
function submit(): void {
  if (props.pending || props.disabled) return
  if (!dirty.value) {
    void cancel()
    return
  }
  attempted.value = true
  if (invalid.value) {
    input.value?.focus()
    return
  }
  submitted.value = true
  emit('submit', Number(draft.value))
}
function enter(event: KeyboardEvent): void {
  if (event.isComposing || event.keyCode === 229) return
  event.preventDefault()
  submit()
}
defineExpose({ cancel })
</script>

<template>
  <div class="modern-inline-number" @keydown.esc.stop.prevent="cancel">
    <PopoverRoot :open="editing" @update:open="requestClose">
      <PopoverAnchor as-child>
        <AppTooltip :label="t('ui.edit')" :disabled="editing || disabled || pending">
          <button
            ref="trigger"
            type="button"
            class="modern-inline-number-display"
            :class="{ 'is-editing': editing }"
            :disabled="disabled || pending"
            :aria-label="label"
            :aria-expanded="editing"
            @click="start"
          >
            <AppIcon :icon="Pencil" size="xs" class="modern-inline-number-hint" />
            <span class="modern-inline-number-figure">{{ modelValue }}</span>
          </button>
        </AppTooltip>
      </PopoverAnchor>
      <PopoverPortal>
        <AppMenuSurface>
          <PopoverContent
            align="end"
            :side-offset="overlaySideOffset"
            :aria-label="label"
            @open-auto-focus.prevent
            @escape-key-down.prevent="cancel"
            @interact-outside="guardOutside"
          >
            <div class="modern-inline-number-editor">
              <AppFieldControl size="xs" :invalid="Boolean(error)" :disabled="disabled || pending">
                <input
                  ref="input"
                  v-model="draft"
                  :aria-label="label"
                  :aria-invalid="Boolean(error) || undefined"
                  :aria-describedby="error ? id : undefined"
                  :disabled="disabled || pending"
                  :inputmode="min < 0 ? 'text' : 'numeric'"
                  @input="emit('clearError')"
                  @keydown.enter="enter"
                />
              </AppFieldControl>
              <AppTooltip :label="t('ui.save')">
                <button
                  type="button"
                  :disabled="disabled || pending"
                  :aria-label="t('ui.save')"
                  @click="submit"
                >
                  <AppIcon
                    :icon="pending ? LoaderCircle : Save"
                    size="xs"
                    :class="{ 'modern-spin': pending }"
                  />
                </button>
              </AppTooltip>
              <AppTooltip :label="t('ui.cancel')">
                <button
                  type="button"
                  :disabled="disabled || pending"
                  :aria-label="t('ui.cancel')"
                  @click="cancel"
                >
                  <AppIcon :icon="X" size="xs" />
                </button>
              </AppTooltip>
            </div>
            <p v-if="error" :id="id" class="modern-inline-number-error" role="alert">
              {{ error }}
            </p>
          </PopoverContent>
        </AppMenuSurface>
      </PopoverPortal>
    </PopoverRoot>
  </div>
</template>

<style scoped>
.modern-inline-number {
  display: flex;
  width: var(--modern-inline-number-width);
  flex: none;
  justify-content: flex-end;
}
.modern-inline-number-display {
  display: inline-flex;
  max-width: 100%;
  min-height: var(--modern-control-xs);
  align-items: center;
  gap: var(--modern-space-1);
  border: 0;
  border-radius: var(--modern-radius-small);
  padding: 0;
  background: transparent;
  color: var(--modern-text);
  font: inherit;
  font-size: var(--modern-font-size-section);
  font-weight: var(--modern-weight-semibold);
  line-height: var(--modern-leading-compact);
  font-variant-numeric: tabular-nums;
  text-align: left;
  cursor: pointer;
}
.modern-inline-number-display:hover:not(:disabled),
.modern-inline-number-display.is-editing {
  color: var(--modern-accent);
}
.modern-inline-number-display:focus-visible {
  outline: var(--modern-focus-width) solid var(--modern-accent);
  outline-offset: var(--modern-focus-offset);
}
.modern-inline-number-display:disabled {
  color: var(--modern-muted);
  cursor: not-allowed;
}
.modern-inline-number-figure {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.modern-inline-number-hint {
  flex: none;
  color: var(--modern-muted);
  opacity: 0;
  transition: opacity var(--modern-motion-fast) var(--modern-motion-ease);
}
.modern-inline-number-display:hover:not(:disabled) .modern-inline-number-hint,
.modern-inline-number-display:focus-visible:not(:disabled) .modern-inline-number-hint,
.modern-inline-number-display.is-editing .modern-inline-number-hint {
  opacity: 1;
}
.modern-inline-number-editor {
  display: flex;
  align-items: center;
  gap: var(--modern-space-1);
  padding: var(--modern-space-2);
}
.modern-inline-number-editor :deep(.modern-field-control) {
  flex: 1;
  min-width: 0;
}
.modern-inline-number-editor input {
  width: 100%;
  min-width: 0;
  border: 0;
  outline: none;
  background: transparent;
  padding: 0;
  font: inherit;
  text-align: left;
  font-variant-numeric: tabular-nums;
}
.modern-inline-number-editor button {
  display: grid;
  min-width: var(--modern-inline-action-target);
  min-height: var(--modern-inline-action-target);
  flex: none;
  place-items: center;
  border: 0;
  border-radius: var(--modern-radius-small);
  background: transparent;
  padding: 0;
  color: var(--modern-muted);
}
.modern-inline-number-editor button:hover:not(:disabled) {
  background: var(--modern-control-hover);
  color: var(--modern-accent);
}
.modern-inline-number-editor button:disabled {
  opacity: var(--modern-opacity-disabled);
  cursor: not-allowed;
}
.modern-inline-number-error {
  max-width: 240px;
  padding: 0 var(--modern-space-2) var(--modern-space-2);
  color: var(--modern-danger);
  font-size: var(--modern-font-size-small);
  overflow-wrap: anywhere;
}
@media (max-width: 760px) {
  .modern-inline-number-display {
    min-width: var(--modern-touch-target);
    min-height: var(--modern-touch-target);
  }
  .modern-inline-number-editor input {
    font-size: var(--modern-font-size-input-mobile);
  }
  .modern-inline-number-editor button {
    min-width: var(--modern-touch-target);
    min-height: var(--modern-touch-target);
  }
}
</style>
