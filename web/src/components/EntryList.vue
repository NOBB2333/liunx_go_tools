<script setup lang="ts">
import { Check, Copy, Eye, File, Folder, FolderOpen } from 'lucide-vue-next'
import { ref, watch } from 'vue'
import { copyText, formatBytes, formatShortTimestamp, formatTimestamp, openItem } from '../api'
import type { Item } from '../types'

const props = withDefaults(defineProps<{ items: Item[]; loading?: boolean; metric?: 'allocated' | 'logical'; allocatedKnown?: boolean; fill?: boolean; resetKey?: string }>(), { metric: 'logical', allocatedKnown: true, fill: false, resetKey: '' })
const emit = defineEmits<{ open: [id: number]; error: [message: string]; preview: [item: Item] }>()
const copiedID = ref<number | null>(null)
const openedID = ref<number | null>(null)
const scroller = ref<HTMLElement | null>(null)

// 翻页、切换目录、改排序都会整体替换列表内容，所以必须像文件管理器那样回到顶部。
// 由父组件通过 prop 声明式驱动，而不是从外部直接操作子组件内部状态。
watch(() => props.resetKey, () => {
  if (scroller.value) scroller.value.scrollTop = 0
})

// 容量只显示当前口径下的一个值：实际占用还是逻辑大小由上面的开关决定，
// 不在每一行再重复一遍。
function itemSize(item: Item): number {
  return props.metric === 'allocated' && props.allocatedKnown ? item.allocated_bytes : item.size_bytes
}

function fullTimeTitle(item: Item): string {
  return `修改 ${formatTimestamp(item.mtime_ns)} | 创建 ${formatTimestamp(item.birthtime_ns)} | 元数据 ${formatTimestamp(item.ctime_ns)}`
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
  <div
    ref="scroller"
    class="relative grid content-start gap-1 overflow-x-hidden overflow-y-auto"
    :class="props.fill ? 'min-h-0 flex-1' : 'h-[60vh] min-h-[360px] max-h-[720px]'"
    :aria-busy="props.loading"
  >
    <div v-if="props.loading && !props.items.length" class="py-6 text-sm text-zinc-500">正在读取目录...</div>
    <div v-else-if="!props.items.length" class="py-6 text-sm text-zinc-500">当前目录为空</div>
    <template v-else>
      <div v-if="props.loading && props.items.length" class="sticky top-0 z-10 justify-self-end rounded bg-zinc-950/90 px-2 py-1 text-xs text-zinc-500">正在读取...</div>
      <!-- 两行：第一行名称 + 容量 + 操作，第二行路径 + 修改时间。
           时间不再和三个时间戳挤在右侧，容量也只留一个。 -->
      <div
        v-for="item in props.items"
        :key="`${item.is_dir ? 'd' : 'f'}-${item.id}`"
        class="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-3 rounded border border-transparent bg-zinc-800 px-3 py-2 text-left text-zinc-200 transition hover:border-emerald-400 hover:bg-zinc-700"
      >
        <button class="min-w-0 text-left" :disabled="!item.is_dir || props.loading" @click="item.is_dir && !props.loading && emit('open', item.id)">
          <span class="flex min-w-0 items-center gap-2 text-sm">
            <Folder v-if="item.is_dir" :size="15" class="shrink-0 text-emerald-300" aria-hidden="true" />
            <File v-else :size="15" class="shrink-0 text-zinc-500" aria-hidden="true" />
            <span class="truncate">{{ item.name }}</span>
          </span>
          <span class="mt-1 flex min-w-0 items-center gap-2 text-xs text-zinc-500">
            <span class="truncate" :title="item.path">{{ item.path || '路径不可用' }}</span>
            <span v-if="item.mtime_ns" class="shrink-0 text-zinc-600" :title="fullTimeTitle(item)">改 {{ formatShortTimestamp(item.mtime_ns) }}</span>
          </span>
        </button>
        <div class="flex shrink-0 items-center gap-1">
          <strong class="mr-1 text-xs font-normal text-zinc-300">{{ formatBytes(itemSize(item)) }}</strong>
          <button v-if="item.path && !item.is_dir" class="flex h-7 w-7 items-center justify-center rounded text-zinc-500 hover:bg-zinc-600 hover:text-white disabled:cursor-not-allowed disabled:opacity-40" :disabled="props.loading" title="预览" aria-label="预览" @click="emit('preview', item)">
            <Eye :size="14" aria-hidden="true" />
          </button>
          <button v-if="item.path" class="flex h-7 w-7 items-center justify-center rounded text-zinc-500 hover:bg-zinc-600 hover:text-white disabled:cursor-not-allowed disabled:opacity-40" :disabled="props.loading" :title="openedID === item.id ? '已在本机打开' : '在本机文件管理器中打开'" :aria-label="openedID === item.id ? '已在本机打开' : '在本机文件管理器中打开'" @click="openPath(item)">
            <Check v-if="openedID === item.id" :size="14" aria-hidden="true" />
            <FolderOpen v-else :size="14" aria-hidden="true" />
          </button>
          <button v-if="item.path" class="flex h-7 w-7 items-center justify-center rounded text-zinc-500 hover:bg-zinc-600 hover:text-white disabled:cursor-not-allowed disabled:opacity-40" :disabled="props.loading" :title="copiedID === item.id ? '已复制路径' : '复制绝对路径'" :aria-label="copiedID === item.id ? '已复制路径' : '复制绝对路径'" @click="copyPath(item)">
            <Check v-if="copiedID === item.id" :size="14" aria-hidden="true" />
            <Copy v-else :size="14" aria-hidden="true" />
          </button>
        </div>
      </div>
    </template>
  </div>
</template>
