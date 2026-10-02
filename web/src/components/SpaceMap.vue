<script setup lang="ts">
import type { Item } from '../types'
import { formatBytes } from '../api'

const props = withDefaults(defineProps<{ items: Item[]; maxSize: number; metric?: 'allocated' | 'logical' }>(), { metric: 'allocated' })
const emit = defineEmits<{ open: [id: number] }>()

function itemSize(item: Item): number {
  return props.metric === 'logical' ? item.size_bytes : item.allocated_bytes
}
</script>

<template>
  <div class="flex min-h-32 flex-wrap gap-1 rounded-md border border-zinc-800 bg-zinc-950 p-1" aria-label="当前目录空间占用图">
    <button v-for="(item, index) in items.slice(0, 24)" :key="item.id" class="flex min-w-24 flex-grow flex-col justify-between overflow-hidden rounded border border-emerald-200/40 p-2 text-left text-xs text-zinc-950 transition hover:brightness-110" :class="index % 3 === 0 ? 'bg-emerald-400' : index % 3 === 1 ? 'bg-sky-400' : 'bg-teal-400'" :style="{ flexBasis: `${Math.max(12, (itemSize(item) / Math.max(1, maxSize)) * 62)}%` }" :title="`${item.name} ${formatBytes(itemSize(item))}`" @click="emit('open', item.id)">
      <span class="truncate font-semibold">{{ item.name }}</span>
      <small>{{ formatBytes(itemSize(item)) }}</small>
    </button>
  </div>
</template>
