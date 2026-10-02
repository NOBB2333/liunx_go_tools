<script setup lang="ts">
import { ArrowUp, Check, ChevronLeft, ChevronRight, Copy, FolderOpen, Home } from 'lucide-vue-next'
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { copyText, formatBytes, formatCount, getChildren, getDirectory, getExtensions, getSummary, openItem } from '../api'
import EntryList from '../components/EntryList.vue'
import SpaceMap from '../components/SpaceMap.vue'
import StatCard from '../components/StatCard.vue'
import type { DirectoryContext, ExtensionStat, Item, PageMeta, Summary } from '../types'

const pageSize = 100
const route = useRoute()
const router = useRouter()
const summary = ref<Summary | null>(null)
const context = ref<DirectoryContext | null>(null)
const children = ref<Item[]>([])
const extensions = ref<ExtensionStat[]>([])
const directory = ref(Math.max(1, Number(route.query.directory || 1)))
const offset = ref(Math.max(0, Number(route.query.offset || 0)))
const pageMeta = ref<PageMeta>({ limit: pageSize, offset: 0, total: 0 })
const sort = ref('size')
const loading = ref(true)
const error = ref('')
const metric = ref<'allocated' | 'logical'>('allocated')
const copiedPath = ref(false)
const openedPath = ref(false)
const directories = computed(() => children.value.filter(item => item.is_dir))
const maxSize = computed(() => Math.max(1, ...directories.value.map(item => metric.value === 'allocated' ? item.allocated_bytes : item.size_bytes)))
const total = computed(() => pageMeta.value.total || 0)
const pageStart = computed(() => total.value === 0 ? 0 : offset.value + 1)
const pageEnd = computed(() => Math.min(offset.value + children.value.length, total.value))

async function loadDirectory(id: number, nextOffset = 0) {
  const scrollY = window.scrollY
  const activeElement = document.activeElement
  if (activeElement instanceof HTMLElement) activeElement.blur()
  loading.value = true
  try {
    const [directoryContext, page] = await Promise.all([
      getDirectory(id),
      getChildren(id, sort.value, nextOffset, pageSize),
    ])
    context.value = directoryContext
    children.value = page.items
    pageMeta.value = page.meta
    directory.value = id
    offset.value = nextOffset
    // Keep the old list's layout during the request and restore the viewport
    // only after the new rows and router query have both rendered.
    loading.value = false
    await router.replace({
      query: {
        ...(id === 1 ? {} : { directory: String(id) }),
        ...(nextOffset === 0 ? {} : { offset: String(nextOffset) }),
      },
    })
    await nextTick()
    await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
    window.scrollTo({ left: window.scrollX, top: scrollY, behavior: 'auto' })
    requestAnimationFrame(() => window.scrollTo({ left: window.scrollX, top: scrollY, behavior: 'auto' }))
    error.value = ''
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '目录加载失败'
  } finally {
    loading.value = false
  }
}

async function openCurrentPath() {
  if (!context.value) return
  try {
    await openItem(context.value.directory.id, true)
    openedPath.value = true
    window.setTimeout(() => { openedPath.value = false }, 1400)
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '无法打开本机文件管理器'
  }
}

async function copyCurrentPath() {
  const path = context.value?.directory.path
  if (!path || !(await copyText(path))) return
  copiedPath.value = true
  window.setTimeout(() => { copiedPath.value = false }, 1400)
}

onMounted(async () => {
  try {
    const [nextSummary, nextExtensions] = await Promise.all([getSummary(), getExtensions()])
    summary.value = nextSummary
    extensions.value = nextExtensions
    await loadDirectory(directory.value, offset.value)
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '快照加载失败'
    loading.value = false
  }
})

watch(() => [route.query.directory, route.query.offset], ([directoryQuery, offsetQuery]) => {
  const nextDirectory = Math.max(1, Number(directoryQuery || 1))
  const nextOffset = Math.max(0, Number(offsetQuery || 0))
  if (nextDirectory !== directory.value || nextOffset !== offset.value) loadDirectory(nextDirectory, nextOffset)
})
</script>

<template>
  <main v-if="summary" class="mx-auto max-w-[1480px] px-5 py-8 lg:px-8">
    <header class="mb-6 flex flex-col items-start justify-between gap-4 sm:flex-row sm:gap-6">
      <div class="min-w-0">
        <div class="text-[11px] font-bold text-emerald-300">FILESYSTEM SNAPSHOT</div>
        <h1 class="mt-2 text-4xl font-semibold text-white">磁盘空间分析</h1>
        <p class="mt-2 break-all text-sm text-zinc-500">{{ summary.manifest.root }}</p>
      </div>
      <span class="shrink-0 rounded border border-emerald-900 px-3 py-2 text-sm text-emerald-300">{{ summary.manifest.complete ? '扫描完成' : '扫描中' }}</span>
    </header>

    <section class="mb-5 grid grid-cols-2 gap-3 lg:grid-cols-5">
      <StatCard label="文件" :value="formatCount(summary.manifest.files)" />
      <StatCard label="目录" :value="formatCount(summary.manifest.directories)" />
      <StatCard label="实际占用" :value="formatBytes(summary.manifest.allocated_bytes)" />
      <StatCard label="逻辑大小" :value="formatBytes(summary.manifest.logical_bytes)" />
      <StatCard label="扫描耗时" :value="`${(summary.manifest.duration_ns / 1e9).toFixed(1)} s`" />
    </section>

    <section class="grid gap-5 lg:grid-cols-[minmax(0,1.5fr)_minmax(340px,.8fr)]">
      <div class="rounded-lg border border-zinc-800 bg-zinc-900 p-5">
        <div class="flex items-start justify-between gap-3">
          <div>
            <div class="text-[11px] font-bold text-emerald-300">DIRECTORY BROWSER</div>
            <h2 class="mt-2 text-xl font-semibold">目录浏览</h2>
          </div>
          <button
            class="flex h-9 w-9 items-center justify-center rounded bg-emerald-400 text-zinc-950 disabled:cursor-not-allowed disabled:opacity-40"
            :disabled="!context || context.directory.parent_id < 1"
            title="返回上级目录"
            aria-label="返回上级目录"
            @click="context && loadDirectory(context.directory.parent_id)"
          >
            <ArrowUp :size="17" aria-hidden="true" />
          </button>
        </div>

        <nav class="my-3 flex min-h-10 items-center gap-1 overflow-x-auto border-y border-zinc-800 py-2 text-sm" aria-label="目录路径">
          <button class="flex h-7 w-7 shrink-0 items-center justify-center rounded text-zinc-400 hover:bg-zinc-800 hover:text-white" title="根目录" aria-label="根目录" @click="loadDirectory(1)">
            <Home :size="15" aria-hidden="true" />
          </button>
          <template v-for="crumb in context?.breadcrumbs || []" :key="crumb.id">
            <span class="text-zinc-700">/</span>
            <button class="max-w-44 shrink-0 truncate rounded px-2 py-1 text-zinc-400 hover:bg-zinc-800 hover:text-white" @click="loadDirectory(crumb.id)">{{ crumb.name }}</button>
          </template>
          <button class="ml-1 flex h-7 w-7 shrink-0 items-center justify-center rounded text-zinc-500 hover:bg-zinc-800 hover:text-white" title="复制当前绝对路径" aria-label="复制当前绝对路径" @click="copyCurrentPath">
            <Check v-if="copiedPath" :size="14" aria-hidden="true" />
            <Copy v-else :size="14" aria-hidden="true" />
          </button>
          <button class="flex h-7 w-7 shrink-0 items-center justify-center rounded text-zinc-500 hover:bg-zinc-800 hover:text-white" :title="openedPath ? '已在本机打开' : '在本机文件管理器中打开当前目录'" :aria-label="openedPath ? '已在本机打开' : '在本机文件管理器中打开当前目录'" @click="openCurrentPath">
            <Check v-if="openedPath" :size="14" aria-hidden="true" />
            <FolderOpen v-else :size="14" aria-hidden="true" />
          </button>
          <select v-model="sort" class="ml-auto shrink-0 rounded border border-zinc-700 bg-zinc-950 px-2 py-1 text-zinc-300" aria-label="目录排序" @change="loadDirectory(directory)">
            <option value="size">按大小</option>
            <option value="name">按名称</option>
            <option value="mtime">按修改时间</option>
          </select>
        </nav>

        <div class="mt-3 flex flex-wrap items-center justify-between gap-3">
          <span class="text-xs text-zinc-500">当前目录空间图与列表使用：</span>
          <div class="flex rounded border border-zinc-700 p-0.5 text-xs" role="group" aria-label="空间计算方式">
            <button class="rounded px-2 py-1" :class="metric === 'allocated' ? 'bg-emerald-400 text-zinc-950' : 'text-zinc-400 hover:text-white'" @click="metric = 'allocated'">实际占用</button>
            <button class="rounded px-2 py-1" :class="metric === 'logical' ? 'bg-emerald-400 text-zinc-950' : 'text-zinc-400 hover:text-white'" @click="metric = 'logical'">逻辑大小</button>
          </div>
        </div>
        <SpaceMap :items="directories" :max-size="maxSize" :metric="metric" @open="id => loadDirectory(id)" />
        <EntryList :key="directory" class="mt-3" :items="children" :loading="loading" :metric="metric" @open="id => loadDirectory(id)" @error="error = $event" />

        <footer class="mt-4 flex min-h-9 items-center justify-between border-t border-zinc-800 pt-4 text-xs text-zinc-500">
          <span>{{ pageStart }}-{{ pageEnd }} / {{ formatCount(total) }}</span>
          <div class="flex gap-1">
            <button class="flex h-8 w-8 items-center justify-center rounded border border-zinc-700 hover:border-emerald-400 hover:text-white disabled:cursor-not-allowed disabled:opacity-30" :disabled="offset === 0 || loading" title="上一页" aria-label="上一页" @click="loadDirectory(directory, Math.max(0, offset - pageSize))">
              <ChevronLeft :size="16" aria-hidden="true" />
            </button>
            <button class="flex h-8 w-8 items-center justify-center rounded border border-zinc-700 hover:border-emerald-400 hover:text-white disabled:cursor-not-allowed disabled:opacity-30" :disabled="offset + children.length >= total || loading" title="下一页" aria-label="下一页" @click="loadDirectory(directory, offset + pageSize)">
              <ChevronRight :size="16" aria-hidden="true" />
            </button>
          </div>
        </footer>
      </div>

      <aside class="grid content-start gap-5">
        <div class="rounded-lg border border-zinc-800 bg-zinc-900 p-5">
          <div class="text-[11px] font-bold text-emerald-300">EXTENSIONS</div>
          <h2 class="mt-2 text-xl font-semibold">类型占用</h2>
          <div class="mt-4 grid gap-3">
            <div v-for="item in extensions" :key="item.extension" class="grid grid-cols-[70px_1fr_auto] items-center gap-2 text-xs">
              <span class="truncate text-zinc-400">{{ item.extension }}</span>
              <span class="h-2 overflow-hidden rounded bg-zinc-800"><i class="block h-full bg-emerald-400" :style="{ width: `${Math.min(100, (item.allocated_bytes / Math.max(1, extensions[0]?.allocated_bytes || 1)) * 100)}%` }" /></span>
              <strong class="font-normal text-zinc-500">{{ formatBytes(item.allocated_bytes) }}</strong>
            </div>
          </div>
        </div>
        <div class="rounded-lg border border-zinc-800 bg-zinc-900 p-5">
          <div class="text-[11px] font-bold text-emerald-300">SCAN STATUS</div>
          <div class="mt-3 grid gap-2 text-sm text-zinc-400">
            <div class="flex justify-between"><span>索引文件</span><span>{{ formatCount(summary.indexed_files) }}</span></div>
            <div class="flex justify-between"><span>索引目录</span><span>{{ formatCount(summary.indexed_directories) }}</span></div>
            <div class="flex justify-between"><span>错误条目</span><span>{{ formatCount(summary.manifest.errors) }}</span></div>
          </div>
        </div>
      </aside>
    </section>
    <p v-if="error" class="mt-4 text-sm text-rose-300">{{ error }}</p>
  </main>
  <main v-else class="mx-auto max-w-[1480px] px-5 py-8 text-zinc-500 lg:px-8">正在加载快照...</main>
</template>
