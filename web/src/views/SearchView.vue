<script setup lang="ts">
import { Check, ChevronLeft, ChevronRight, Copy, FolderOpen, Search } from 'lucide-vue-next'
import { nextTick, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { copyText, formatBytes, formatTimestamp, getSummary, openItem, searchFiles } from '../api'
import type { Item } from '../types'

const pageSize = 100
const route = useRoute()
const router = useRouter()
const query = ref(String(route.query.q || ''))
const results = ref<Item[]>([])
const offset = ref(Math.max(0, Number(route.query.offset || 0)))
const loading = ref(false)
const error = ref('')
const copiedID = ref<number | null>(null)
const openedID = ref<number | null>(null)
const allocatedKnown = ref(false)

async function loadPage(nextOffset: number) {
  const scrollY = window.scrollY
  const activeElement = document.activeElement
  if (activeElement instanceof HTMLElement) activeElement.blur()
  const term = query.value.trim()
  if (!term) {
    results.value = []
    offset.value = 0
    return
  }
  loading.value = true
  try {
    const page = await searchFiles(term, nextOffset, pageSize)
    results.value = page.items
    offset.value = nextOffset
    await router.replace({ query: { q: term, ...(nextOffset === 0 ? {} : { offset: String(nextOffset) }) } })
    await nextTick()
    await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
    window.scrollTo({ left: window.scrollX, top: scrollY, behavior: 'auto' })
    requestAnimationFrame(() => window.scrollTo({ left: window.scrollX, top: scrollY, behavior: 'auto' }))
    error.value = ''
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '搜索失败'
  } finally {
    loading.value = false
  }
}

async function openPath(item: Item) {
  try {
    await openItem(item.id, item.is_dir)
    openedID.value = item.id
    window.setTimeout(() => { if (openedID.value === item.id) openedID.value = null }, 1400)
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '无法打开本机文件管理器'
  }
}

async function copyPath(item: Item) {
  if (!item.path || !(await copyText(item.path))) return
  copiedID.value = item.id
  window.setTimeout(() => { if (copiedID.value === item.id) copiedID.value = null }, 1400)
}

function submit() {
  void loadPage(0)
}

onMounted(async () => {
  try {
    const summary = await getSummary()
    allocatedKnown.value = summary.manifest.allocated_bytes_known
  } catch {
    allocatedKnown.value = false
  }
  if (query.value.trim()) void loadPage(offset.value)
})
</script>

<template>
  <main class="mx-auto max-w-[1000px] px-5 py-8 lg:px-8">
    <div class="text-[11px] font-bold text-emerald-300">SEARCH</div>
    <h1 class="mt-2 text-3xl font-semibold">文件搜索</h1>
    <form class="mt-6 flex gap-2" @submit.prevent="submit">
      <input v-model="query" class="min-w-0 flex-1 rounded border border-zinc-700 bg-zinc-900 px-3 py-2 text-zinc-100 outline-none focus:border-emerald-400" placeholder="输入文件名关键词" aria-label="文件名关键词" />
      <button class="flex h-10 w-10 shrink-0 items-center justify-center rounded bg-emerald-400 text-zinc-950" title="搜索" aria-label="搜索">
        <Search :size="18" aria-hidden="true" />
      </button>
    </form>
    <p v-if="error" class="mt-4 text-sm text-rose-300">{{ error }}</p>
    <div class="mt-6 rounded-lg border border-zinc-800 bg-zinc-900 p-5">
      <div v-if="loading && !results.length" class="min-h-24 text-zinc-500">正在搜索...</div>
      <div v-else-if="!results.length" class="min-h-24 text-zinc-500">暂无结果</div>
      <div v-else class="grid gap-2">
        <div v-for="item in results" :key="item.id" class="grid min-w-0 gap-2 rounded bg-zinc-800 px-3 py-2 text-sm lg:grid-cols-[minmax(0,1fr)_auto_auto] lg:items-center">
          <span class="min-w-0">
            <span class="block truncate">{{ item.name }}</span>
            <span class="block truncate text-xs text-zinc-500">{{ item.path || '路径不可用' }}</span>
          </span>
          <span class="flex shrink-0 items-baseline gap-2 text-xs">
            <strong class="font-normal text-zinc-300">{{ formatBytes(allocatedKnown ? item.allocated_bytes : item.size_bytes) }}</strong>
            <span v-if="!allocatedKnown" class="text-zinc-500">逻辑大小 · 实际占用不可用</span>
            <span v-else class="text-zinc-600">逻辑 {{ formatBytes(item.size_bytes) }}</span>
          </span>
          <span class="flex shrink-0 items-center justify-between gap-3 lg:justify-end">
            <span class="max-w-[280px] truncate text-[11px] text-zinc-500" :title="`修改 ${formatTimestamp(item.mtime_ns)} | 创建 ${formatTimestamp(item.birthtime_ns)} | 元数据 ${formatTimestamp(item.ctime_ns)}`">改 {{ formatTimestamp(item.mtime_ns) }} · 建 {{ formatTimestamp(item.birthtime_ns) }} · 元 {{ formatTimestamp(item.ctime_ns) }}</span>
            <button v-if="item.path" class="flex h-7 w-7 items-center justify-center rounded text-zinc-500 hover:bg-zinc-700 hover:text-white" :title="openedID === item.id ? '已在本机打开' : '在本机文件管理器中打开'" :aria-label="openedID === item.id ? '已在本机打开' : '在本机文件管理器中打开'" @click="openPath(item)">
              <Check v-if="openedID === item.id" :size="14" aria-hidden="true" />
              <FolderOpen v-else :size="14" aria-hidden="true" />
            </button>
            <button v-if="item.path" class="flex h-7 w-7 items-center justify-center rounded text-zinc-500 hover:bg-zinc-700 hover:text-white" :title="copiedID === item.id ? '已复制路径' : '复制绝对路径'" :aria-label="copiedID === item.id ? '已复制路径' : '复制绝对路径'" @click="copyPath(item)">
              <Check v-if="copiedID === item.id" :size="14" aria-hidden="true" />
              <Copy v-else :size="14" aria-hidden="true" />
            </button>
          </span>
        </div>
      </div>
      <footer class="mt-4 flex min-h-9 items-center justify-between border-t border-zinc-800 pt-4 text-xs text-zinc-500">
        <span v-if="results.length">第 {{ offset + 1 }}-{{ offset + results.length }} 条</span>
        <span v-else />
        <div class="flex gap-1">
          <button class="flex h-8 w-8 items-center justify-center rounded border border-zinc-700 hover:border-emerald-400 hover:text-white disabled:cursor-not-allowed disabled:opacity-30" :disabled="offset === 0 || loading" title="上一页" aria-label="上一页" @click="loadPage(Math.max(0, offset - pageSize))">
            <ChevronLeft :size="16" aria-hidden="true" />
          </button>
          <button class="flex h-8 w-8 items-center justify-center rounded border border-zinc-700 hover:border-emerald-400 hover:text-white disabled:cursor-not-allowed disabled:opacity-30" :disabled="results.length < pageSize || loading" title="下一页" aria-label="下一页" @click="loadPage(offset + pageSize)">
            <ChevronRight :size="16" aria-hidden="true" />
          </button>
        </div>
      </footer>
    </div>
  </main>
</template>
