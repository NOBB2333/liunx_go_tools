<script setup lang="ts">
import type { Item } from '../types'
import { formatBytes } from '../api'

const props = withDefaults(defineProps<{ items: Item[]; maxSize: number; metric?: 'allocated' | 'logical'; allocatedKnown?: boolean; limit?: number }>(), { metric: 'allocated', allocatedKnown: true, limit: 24 })
const emit = defineEmits<{ open: [id: number] }>()

function itemSize(item: Item): number {
  // 必须和父组件的 maxSize 保持一致：实际占用不可用时两边都要回退到逻辑大小，
  // 否则方块宽度会和归一化基准用的是两个不同的数字。
  return props.metric === 'logical' || !props.allocatedKnown ? item.size_bytes : item.allocated_bytes
}
</script>

<template>
  <div
    class="flex min-h-32 flex-wrap gap-1 rounded-md border border-zinc-800 bg-zinc-950 p-1"
    aria-label="当前目录空间占用图"
  >
    <button v-for="(item, index) in items.slice(0, props.limit)" :key="item.id" class="flex min-w-24 flex-grow flex-col justify-between overflow-hidden rounded border border-emerald-200/40 p-2 text-left text-xs text-zinc-950 transition hover:brightness-110" :class="index % 3 === 0 ? 'bg-emerald-400' : index % 3 === 1 ? 'bg-sky-400' : 'bg-teal-400'" :style="{ flexBasis: `${Math.max(12, (itemSize(item) / Math.max(1, maxSize)) * 62)}%` }" :title="`${item.name} ${formatBytes(itemSize(item))}`" @click="emit('open', item.id)">
      <span class="truncate font-semibold">{{ item.name }}</span>
      <small>{{ formatBytes(itemSize(item)) }}</small>
    </button>
  </div>
</template>
