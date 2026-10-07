<script setup lang="ts">
import { ChevronDown, ChevronRight } from 'lucide-vue-next'
import { useUiPrefs } from '../composables/useUiPrefs'

const props = withDefaults(defineProps<{ id: string; eyebrow?: string; title: string; hint?: string; collapsible?: boolean }>(), { eyebrow: '', hint: '', collapsible: true })
const { isCollapsed, toggleCollapsed } = useUiPrefs()
</script>

<template>
  <div class="min-w-0 max-w-full overflow-hidden rounded-lg border border-zinc-800 bg-zinc-900 p-5">
    <div class="flex items-start justify-between gap-3">
      <div class="min-w-0">
        <div v-if="props.eyebrow" class="text-[11px] font-bold text-emerald-300">{{ props.eyebrow }}</div>
        <h2 class="font-semibold" :class="props.eyebrow ? 'mt-1 text-base' : 'text-base'">{{ props.title }}</h2>
        <p v-if="props.hint && !isCollapsed(props.id)" class="mt-1 min-w-0 break-words text-xs text-zinc-500">{{ props.hint }}</p>
      </div>
      <button
        v-if="props.collapsible"
        class="flex h-7 w-7 shrink-0 items-center justify-center rounded text-zinc-500 hover:bg-zinc-800 hover:text-white"
        :title="isCollapsed(props.id) ? `展开${props.title}` : `收起${props.title}`"
        :aria-label="isCollapsed(props.id) ? `展开${props.title}` : `收起${props.title}`"
        :aria-expanded="!isCollapsed(props.id)"
        @click="toggleCollapsed(props.id)"
      >
        <ChevronRight v-if="isCollapsed(props.id)" :size="15" aria-hidden="true" />
        <ChevronDown v-else :size="15" aria-hidden="true" />
      </button>
    </div>
    <!-- 折叠只是隐藏，不卸载内容，这样展开时不用重新请求 -->
    <div v-show="!isCollapsed(props.id)" class="mt-3">
      <slot />
    </div>
  </div>
</template>
