<script setup lang="ts">
import { FileViewer } from '@file-viewer/vue3'
import { FolderOpen } from 'lucide-vue-next'
import { computed, ref, shallowRef, watch } from 'vue'
import { fetchContentFile, formatBytes, getContentURL, openItem } from '../api'
import type { Item } from '../types'

// 这些格式的渲染器是按需拉流的（靠 HTTP Range），多大都能预览，不设上限
const STREAMABLE = new Set(['.mp4', '.webm', '.mkv', '.mov', '.avi', '.m4v', '.mp3', '.wav', '.flac', '.ogg', '.m4a', '.aac', '.pdf'])
// 小文件先取成 File 再交给预览器：响应只被当作数据解析，浏览器不会去「导航」内容地址
const INLINE_LIMIT = 32 * 1024 * 1024
// 其余格式会被渲染器整个读进内存解析，超过这个大小就别在浏览器里试了
const MEMORY_LIMIT = 1024 * 1024 * 1024

const props = withDefaults(defineProps<{ item: Item | null; metric?: 'allocated' | 'logical'; allocatedKnown?: boolean }>(), { metric: 'logical', allocatedKnown: true })
const emit = defineEmits<{ error: [message: string] }>()

// File 是浏览器原生对象，不需要深层响应式，用 shallowRef 免得被代理包一层
const file = shallowRef<File | null>(null)
const loading = ref(false)
const failed = ref('')
const opened = ref(false)

const size = computed(() => !props.item ? 0 : (props.metric === 'allocated' && props.allocatedKnown ? props.item.allocated_bytes : props.item.size_bytes))
const extension = computed(() => (props.item?.name || '').toLowerCase().replace(/^.*(\.[^.]*)$/, '$1'))
const streamable = computed(() => STREAMABLE.has(extension.value))
const mode = computed<'stream' | 'file' | 'blocked' | 'none'>(() => {
  if (!props.item || props.item.is_dir) return 'none'
  if (streamable.value) return 'stream'
  if (size.value <= INLINE_LIMIT) return 'file'
  if (size.value <= MEMORY_LIMIT) return 'stream'
  return 'blocked'
})

// 抽屉一打开就直接预览，不再让用户多点一次按钮。
// 换文件时先把上一份丢掉，否则会残留上一个文档的内容。
watch(() => props.item?.id, () => {
  file.value = null
  failed.value = ''
  if (mode.value === 'file') void load()
}, { immediate: true })

async function load() {
  const target = props.item
  if (!target || mode.value !== 'file' || loading.value) return
  loading.value = true
  failed.value = ''
  try {
    file.value = await fetchContentFile(target)
  } catch (cause) {
    failed.value = cause instanceof Error ? cause.message : '读取文件失败'
  } finally {
    loading.value = false
  }
}

async function openInFileManager() {
  const target = props.item
  if (!target) return
  try {
    await openItem(target.id, target.is_dir)
    opened.value = true
    window.setTimeout(() => { opened.value = false }, 1400)
  } catch (cause) {
    emit('error', cause instanceof Error ? cause.message : '无法打开本机文件管理器')
  }
}
</script>

<template>
  <div class="flex h-full min-h-0 w-full flex-col gap-3">
    <div class="flex min-w-0 shrink-0 flex-wrap items-center gap-2 text-xs text-zinc-500">
      <span class="shrink-0">{{ formatBytes(size) }}</span>
      <span class="min-w-0 truncate" :title="props.item?.path">{{ props.item?.path }}</span>
      <button v-if="props.item" class="ml-auto flex shrink-0 items-center gap-1 rounded border border-zinc-700 px-2 py-1 text-zinc-400 hover:border-emerald-400 hover:text-white" @click="openInFileManager">
        <FolderOpen :size="13" aria-hidden="true" />
        <span>{{ opened ? '已在本机打开' : '在本机打开' }}</span>
      </button>
    </div>
    <div v-if="loading" class="py-6 text-sm text-zinc-500">正在读取文件...</div>
    <p v-else-if="props.item?.is_dir" class="text-sm text-zinc-500">这是目录，没有可预览的内容</p>
    <p v-else-if="mode === 'blocked'" class="text-sm text-amber-300">
      这个格式要在内存里整份解析，文件超过 {{ formatBytes(MEMORY_LIMIT) }} 就不再尝试，请用「在本机打开」。
    </p>
    <p v-else-if="failed" class="text-sm text-rose-300">{{ failed }}</p>
    <!-- 大文件走 url：预览器自己按 Range 拉流，不用先把整份读进内存。
         内容地址是带 CSP: sandbox 的，即使被当作文档打开也执行不了脚本。 -->
    <FileViewer
      v-else-if="mode === 'stream'"
      :url="getContentURL(props.item!)"
      :name="props.item!.name"
      :filename="props.item!.name"
      :size="size"
      :options="{ theme: 'dark' }"
      class="min-h-0 w-full flex-1"
      @error="failed = $event"
    />
    <!-- FileViewer 挂在 v-if 上：组件卸载会自动销毁渲染会话并释放 Worker / WASM -->
    <FileViewer v-else-if="file" :file="file" :name="props.item?.name" :options="{ theme: 'dark' }" class="min-h-0 w-full flex-1" @error="failed = $event" />
  </div>
</template>
