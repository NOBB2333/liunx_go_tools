<script setup lang="ts">
import { X } from 'lucide-vue-next'
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import FilePreview from './FilePreview.vue'
import type { Item } from '../types'

const MIN_WIDTH = 380
// 左边至少留这么多，方便还能看到并操作文件列表
const MIN_LIST_VISIBLE = 160
const STORAGE_KEY = 'golangtools.filesystem.previewWidth'

const props = withDefaults(defineProps<{ item: Item | null; metric?: 'allocated' | 'logical'; allocatedKnown?: boolean }>(), { metric: 'logical', allocatedKnown: true })
const emit = defineEmits<{ close: []; error: [message: string] }>()

function maxWidth() {
  return Math.max(MIN_WIDTH, window.innerWidth - MIN_LIST_VISIBLE)
}

function readWidth(): number {
  try {
    const raw = Number(window.localStorage.getItem(STORAGE_KEY))
    if (Number.isFinite(raw) && raw >= MIN_WIDTH) return raw
  } catch {
    // 隐私模式下读不到就用默认值
  }
  return Math.min(760, maxWidth())
}

// 有些文件要看得宽一点，有些窄一点就够，所以宽度由用户拖，并且记住
const width = ref(readWidth())
const dragging = ref(false)

function clamp() {
  width.value = Math.min(Math.max(width.value, MIN_WIDTH), maxWidth())
}

function startDrag(event: PointerEvent) {
  event.preventDefault()
  const startX = event.clientX
  const startWidth = width.value
  dragging.value = true
  const onMove = (moveEvent: PointerEvent) => {
    // 往左拖是变宽
    width.value = Math.min(Math.max(startWidth + (startX - moveEvent.clientX), MIN_WIDTH), maxWidth())
  }
  const onUp = () => {
    dragging.value = false
    window.removeEventListener('pointermove', onMove)
    window.removeEventListener('pointerup', onUp)
    try {
      window.localStorage.setItem(STORAGE_KEY, String(Math.round(width.value)))
    } catch {
      // 记不住也无所谓，不影响使用
    }
  }
  window.addEventListener('pointermove', onMove)
  window.addEventListener('pointerup', onUp)
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') emit('close')
}

watch(() => props.item, item => {
  if (item) window.addEventListener('keydown', onKeydown)
  else window.removeEventListener('keydown', onKeydown)
}, { immediate: true })

onMounted(() => window.addEventListener('resize', clamp))
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
  window.removeEventListener('resize', clamp)
})
</script>

<template>
  <!-- 用 Teleport 挂到 body 上，避免被左卡片的 flex 约束或 overflow 裁掉 -->
  <Teleport to="body">
    <div v-if="props.item" class="fixed inset-0 z-50 flex justify-end" role="dialog" aria-modal="true" aria-label="文件预览">
      <div class="absolute inset-0 bg-black/60" @click="emit('close')" />
      <section
        class="relative flex h-full max-w-full flex-col border-l border-zinc-800 bg-zinc-950 shadow-2xl"
        :style="{ width: `${width}px` }"
      >
        <!-- 左侧拖拽手柄 -->
        <div
          class="absolute inset-y-0 left-0 z-10 w-1.5 cursor-col-resize bg-transparent transition hover:bg-emerald-400/60"
          :class="dragging ? 'bg-emerald-400/80' : ''"
          role="separator"
          aria-orientation="vertical"
          aria-label="拖动调整预览宽度"
          title="拖动调整预览宽度"
          @pointerdown="startDrag"
        />
        <header class="flex shrink-0 items-center justify-between gap-3 border-b border-zinc-800 px-5 py-3 pl-6">
          <div class="min-w-0">
            <div class="truncate text-sm font-semibold text-zinc-100" :title="props.item.name">{{ props.item.name }}</div>
            <div class="truncate text-xs text-zinc-600" :title="props.item.path">{{ props.item.path || '路径不可用' }}</div>
          </div>
          <button class="flex h-8 w-8 shrink-0 items-center justify-center rounded text-zinc-400 hover:bg-zinc-800 hover:text-white" title="关闭预览" aria-label="关闭预览" @click="emit('close')">
            <X :size="17" aria-hidden="true" />
          </button>
        </header>
        <div class="min-h-0 flex-1 overflow-hidden p-4">
          <FilePreview :item="props.item" :metric="props.metric" :allocated-known="props.allocatedKnown" @error="emit('error', $event)" />
        </div>
      </section>
    </div>
  </Teleport>
</template>
