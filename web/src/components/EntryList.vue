<script setup lang="ts">
import { Check, Copy, File, Folder, FolderOpen } from 'lucide-vue-next'
import { ref } from 'vue'
import { copyText, formatBytes, formatTimestamp, openItem } from '../api'
import type { Item } from '../types'

const props = withDefaults(defineProps<{ items: Item[]; loading?: boolean; metric?: 'allocated' | 'logical' }>(), { metric: 'allocated' })
const emit = defineEmits<{ open: [id: number]; error: [message: string] }>()
const copiedID = ref<number | null>(null)
const openedID = ref<number | null>(null)

function itemSize(item: Item): number {
  return props.metric === 'logical' ? item.size_bytes : item.allocated_bytes
}

async function copyPath(item: Item) {
  if (!item.path || !(await copyText(item.path))) return
  copiedID.value = item.id
  window.setTimeout(() => {
    if (copiedID.value === item.id) copiedID.value = null
  }, 1400)
}

async function openPath(item: Item) {
  try {
    await openItem(item.id, item.is_dir)
    openedID.value = item.id
    window.setTimeout(() => {
      if (openedID.value === item.id) openedID.value = null
    }, 1400)
  } catch (cause) {
    emit('error', cause instanceof Error ? cause.message : '无法打开本机文件管理器')
  }
}
</script>

<template>
  <div class="relative grid h-[60vh] min-h-[360px] max-h-[720px] content-start gap-1 overflow-y-auto" :aria-busy="props.loading">
    <div v-if="props.loading && !props.items.length" class="py-6 text-sm text-zinc-500">正在读取目录...</div>
    <div v-else-if="!props.items.length" class="py-6 text-sm text-zinc-500">当前目录为空</div>
    <template v-else>
      <div v-if="props.loading && props.items.length" class="sticky top-0 z-10 justify-self-end rounded bg-zinc-950/90 px-2 py-1 text-xs text-zinc-500">正在读取...</div>
      <div
        v-for="item in props.items"
        :key="`${item.is_dir ? 'd' : 'f'}-${item.id}`"
        class="grid min-w-0 gap-2 rounded border border-transparent bg-zinc-800 px-3 py-2 text-left text-sm text-zinc-200 transition hover:border-emerald-400 hover:bg-zinc-700 lg:grid-cols-[minmax(0,1fr)_auto_auto] lg:items-center"
      >
      <button class="flex min-w-0 items-start gap-2 text-left" :title="item.is_dir ? `进入 ${item.path || item.name}` : item.path" :disabled="!item.is_dir || props.loading" @click="item.is_dir && !props.loading && emit('open', item.id)">
        <Folder v-if="item.is_dir" :size="16" class="mt-0.5 shrink-0 text-emerald-300" aria-hidden="true" />
        <File v-else :size="16" class="mt-0.5 shrink-0 text-zinc-500" aria-hidden="true" />
        <span class="min-w-0">
          <span class="block truncate">{{ item.name }}</span>
          <span class="mt-0.5 block truncate text-xs text-zinc-500">{{ item.path || '路径不可用' }}</span>
        </span>
      </button>
      <span class="flex shrink-0 items-baseline gap-2 text-xs lg:text-right">
        <strong class="font-normal text-zinc-300">{{ formatBytes(itemSize(item)) }}</strong>
        <span class="text-zinc-600">逻辑 {{ formatBytes(item.size_bytes) }}</span>
      </span>
      <div class="flex shrink-0 items-center justify-between gap-3 lg:justify-end">
        <span v-if="!item.is_dir" class="max-w-[280px] truncate text-[11px] text-zinc-500" :title="`修改 ${formatTimestamp(item.mtime_ns)} | 创建 ${formatTimestamp(item.birthtime_ns)} | 元数据 ${formatTimestamp(item.ctime_ns)}`">改 {{ formatTimestamp(item.mtime_ns) }} · 建 {{ formatTimestamp(item.birthtime_ns) }} · 元 {{ formatTimestamp(item.ctime_ns) }}</span>
        <button v-if="item.path" class="flex h-7 w-7 shrink-0 items-center justify-center rounded text-zinc-500 hover:bg-zinc-600 hover:text-white disabled:cursor-not-allowed disabled:opacity-40" :disabled="props.loading" :title="openedID === item.id ? '已在本机打开' : '在本机文件管理器中打开'" :aria-label="openedID === item.id ? '已在本机打开' : '在本机文件管理器中打开'" @click="openPath(item)">
          <Check v-if="openedID === item.id" :size="14" aria-hidden="true" />
          <FolderOpen v-else :size="14" aria-hidden="true" />
        </button>
        <button v-if="item.path" class="flex h-7 w-7 shrink-0 items-center justify-center rounded text-zinc-500 hover:bg-zinc-600 hover:text-white disabled:cursor-not-allowed disabled:opacity-40" :disabled="props.loading" :title="copiedID === item.id ? '已复制路径' : '复制绝对路径'" :aria-label="copiedID === item.id ? '已复制路径' : '复制绝对路径'" @click="copyPath(item)">
          <Check v-if="copiedID === item.id" :size="14" aria-hidden="true" />
          <Copy v-else :size="14" aria-hidden="true" />
        </button>
      </div>
      </div>
    </template>
  </div>
</template>
