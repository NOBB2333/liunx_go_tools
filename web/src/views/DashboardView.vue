<script setup lang="ts">
import { ArrowUp, Check, ChevronDown, ChevronLeft, ChevronRight, Copy, FolderOpen, Home } from 'lucide-vue-next'
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { copyText, formatBytes, formatCount, getChildren, getDirectory, getExtensions, getSummary, openItem } from '../api'
import EntryList from '../components/EntryList.vue'
import PanelCard from '../components/PanelCard.vue'
import PreviewDrawer from '../components/PreviewDrawer.vue'
import SpaceMap from '../components/SpaceMap.vue'
import StatCard from '../components/StatCard.vue'
import { useUiPrefs } from '../composables/useUiPrefs'
import type { DirectoryContext, ExtensionStat, Item, PageMeta, Summary } from '../types'

const pageSize = 100
// 右栏「当前目录最大文件」列多少个；后端一次多取一些再过滤出文件
const topFileCount = 8
const topFileFetch = 20

const route = useRoute()
const router = useRouter()
const { isCollapsed, toggleCollapsed } = useUiPrefs()
const summary = ref<Summary | null>(null)
const context = ref<DirectoryContext | null>(null)
const children = ref<Item[]>([])
const extensions = ref<ExtensionStat[]>([])
const topFiles = ref<Item[]>([])
const previewItem = ref<Item | null>(null)
const directory = ref(Math.max(1, Number(route.query.directory || 1)))
const offset = ref(Math.max(0, Number(route.query.offset || 0)))
const pageMeta = ref<PageMeta>({ limit: pageSize, offset: 0, total: 0 })
const sort = ref('size')
const loading = ref(true)
const error = ref('')
const metric = ref<'allocated' | 'logical'>('logical')
const copiedPath = ref(false)
const openedPath = ref(false)
const extensionsByCount = ref(false)

// 空间图收起来之后，列表就吃满整张卡片。同一时刻整页头部和统计卡也让位，
// 否则右栏面板一多仍会把整页撑高，又变回「页面滚动 + 列表内滚动」两层滚动条。
const mapCollapsed = computed(() => isCollapsed('spacemap'))
const filled = computed(() => mapCollapsed.value)
const allocatedKnown = computed(() => summary.value?.manifest.allocated_bytes_known === true)
const directories = computed(() => children.value.filter(item => item.is_dir))

function itemSize(item: Item): number {
  return metric.value === 'allocated' && allocatedKnown.value ? item.allocated_bytes : item.size_bytes
}

const maxSize = computed(() => Math.max(1, ...directories.value.map(itemSize)))
const topFilesMax = computed(() => Math.max(1, ...topFiles.value.map(itemSize)))
const extensionSize = (item?: ExtensionStat) => item ? (allocatedKnown.value ? item.allocated_bytes : item.size_bytes) : 0
// 空间图只能拿到当前页 children 里的目录，所以上限必须明说而不是隐含；
// 收起后那条摘要也要自己按大小重排 —— directories 跟随的是用户选的列表排序。
const mapLimit = 24
const mappedCount = computed(() => Math.min(mapLimit, directories.value.length))
const topDirectoriesLabel = computed(() => {
  const top = [...directories.value].sort((left, right) => itemSize(right) - itemSize(left)).slice(0, 3)
  return top.length ? top.map(item => `${item.name} ${formatBytes(itemSize(item))}`).join(' · ') : '当前目录没有子目录'
})
// 扩展名条形图的基准：按大小用最大的一项，按数量用最多的一项
const extensionBasis = computed(() => Math.max(1, ...extensions.value.map(item => extensionsByCount.value ? item.files : extensionSize(item)), 1))
const extensionValue = (item: ExtensionStat) => extensionsByCount.value ? item.files : extensionSize(item)
const extensionText = (item: ExtensionStat) => extensionsByCount.value ? `${formatCount(item.files)} 个` : formatBytes(extensionSize(item))
const total = computed(() => pageMeta.value.total || 0)
const pageStart = computed(() => total.value === 0 ? 0 : offset.value + 1)
const pageEnd = computed(() => Math.min(offset.value + children.value.length, total.value))
// 递归统计值由 breadcrumbs 一并返回。旧快照没有这两个字段，恒为 0，
// 所以要区分「真的是空目录」和「没记录」。
const overview = computed(() => {
  const dir = context.value?.directory
  if (!dir) return null
  const pageDirs = children.value.filter(item => item.is_dir).length
  return {
    dir,
    pageDirs,
    pageFiles: children.value.length - pageDirs,
    recursiveSize: metric.value === 'allocated' && allocatedKnown.value ? (dir.allocated_bytes ?? 0) : (dir.size_bytes ?? 0),
    countsMissing: (dir.file_count ?? 0) === 0 && (dir.dir_count ?? 0) === 0 && total.value > 0,
  }
})

async function loadDirectory(id: number, nextOffset = 0) {
  const scrollY = window.scrollY
  const activeElement = document.activeElement
  if (activeElement instanceof HTMLElement) activeElement.blur()
  loading.value = true
  try {
    const [directoryContext, page, topPage] = await Promise.all([
      getDirectory(id),
      getChildren(id, sort.value, nextOffset, pageSize),
      getChildren(id, 'size', 0, topFileFetch),
    ])
    context.value = directoryContext
    children.value = page.items
    pageMeta.value = page.meta
    // 无论列表当前按什么排序，这一栏始终按大小取，所以单独要一份
    topFiles.value = topPage.items.filter(item => !item.is_dir).slice(0, topFileCount)
    directory.value = id
    offset.value = nextOffset
    // 请求期间保留旧列表的布局，等新行和路由 query 都渲染完再恢复视口位置。
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

function showPreview(item: Item) {
  previewItem.value = item
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
    metric.value = nextSummary.manifest.allocated_bytes_known ? 'allocated' : 'logical'
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
    <!-- 空间图收起后整页头部和统计卡让位给列表高度 -->
    <header v-if="!filled" class="mb-6 flex flex-col items-start justify-between gap-4 sm:flex-row sm:gap-6">
      <div class="min-w-0">
        <div class="text-[11px] font-bold text-emerald-300">FILESYSTEM SNAPSHOT</div>
        <h1 class="mt-2 text-4xl font-semibold text-white">磁盘空间分析</h1>
        <p class="mt-2 break-all text-sm text-zinc-500">{{ summary.manifest.root }}</p>
      </div>
      <span class="shrink-0 rounded border border-emerald-900 px-3 py-2 text-sm text-emerald-300">{{ summary.manifest.complete ? '扫描完成' : '扫描中' }}</span>
    </header>

    <section v-if="!filled" class="mb-5 grid grid-cols-2 gap-3 lg:grid-cols-5">
      <StatCard label="文件" :value="formatCount(summary.manifest.files)" />
      <StatCard label="目录" :value="formatCount(summary.manifest.directories)" />
      <StatCard label="实际占用" :value="allocatedKnown ? formatBytes(summary.manifest.allocated_bytes) : '不可用'" />
      <StatCard label="逻辑大小" :value="formatBytes(summary.manifest.logical_bytes)" />
      <StatCard label="扫描耗时" :value="`${(summary.manifest.duration_ns / 1e9).toFixed(1)} s`" />
    </section>

    <section class="grid gap-5 lg:grid-cols-[minmax(0,1.5fr)_minmax(340px,.8fr)]">
      <!-- 左卡片用 flex 列布局，让列表能吃掉剩余高度 -->
      <div class="flex min-w-0 flex-col rounded-lg border border-zinc-800 bg-zinc-900" :class="filled ? 'h-[calc(100dvh-10rem)] min-h-[520px] p-4' : 'p-5'">
        <div class="flex shrink-0 items-start justify-between gap-3">
          <div class="min-w-0">
            <div v-if="!filled" class="text-[11px] font-bold text-emerald-300">DIRECTORY BROWSER</div>
            <h2 class="font-semibold" :class="filled ? 'text-lg' : 'mt-2 text-xl'">目录浏览</h2>
          </div>
          <div class="flex shrink-0 items-center gap-2">
            <span v-if="filled" class="rounded border px-2 py-1 text-xs" :class="summary.manifest.complete ? 'border-emerald-900 text-emerald-300' : 'border-amber-900 text-amber-300'">{{ summary.manifest.complete ? '扫描完成' : '扫描中' }}</span>
            <button
              class="flex items-center justify-center rounded bg-emerald-400 text-zinc-950 disabled:cursor-not-allowed disabled:opacity-40"
              :class="filled ? 'h-8 w-8' : 'h-9 w-9'"
              :disabled="!context || context.directory.parent_id < 1"
              title="返回上级目录"
              aria-label="返回上级目录"
              @click="context && loadDirectory(context.directory.parent_id)"
            >
              <ArrowUp :size="17" aria-hidden="true" />
            </button>
          </div>
        </div>

        <nav class="flex min-h-10 shrink-0 items-center gap-1 overflow-x-auto border-y border-zinc-800 py-2 text-sm" :class="filled ? 'my-2' : 'my-3'" aria-label="目录路径">
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

        <div v-if="!filled" class="flex shrink-0 flex-wrap items-center justify-between gap-3">
          <span class="text-xs text-zinc-500">当前目录空间图与列表使用：{{ allocatedKnown ? '' : '实际占用不可用，已使用逻辑大小' }}</span>
        </div>

        <!-- 空间图用百叶窗折叠；收起后整条标题行本身就是展开入口 -->
        <div class="flex shrink-0 items-center justify-between gap-2 text-xs text-zinc-500" :class="filled ? 'mt-1' : 'mt-3'">
          <button
            class="flex min-w-0 flex-1 items-center gap-1 rounded px-1 py-1 text-left hover:bg-zinc-800/60 hover:text-zinc-300"
            :title="mapCollapsed ? '展开空间占用图' : '收起空间占用图'"
            :aria-label="mapCollapsed ? '展开空间占用图' : '收起空间占用图'"
            :aria-expanded="!mapCollapsed"
            @click="toggleCollapsed('spacemap')"
          >
            <ChevronRight v-if="mapCollapsed" :size="14" class="shrink-0" aria-hidden="true" />
            <ChevronDown v-else :size="14" class="shrink-0" aria-hidden="true" />
            <span class="min-w-0 truncate" :title="mapCollapsed ? topDirectoriesLabel : ''">
              <template v-if="mapCollapsed">最大：{{ topDirectoriesLabel }}</template>
              <template v-else>空间占用图 · {{ mappedCount }} / {{ directories.length }} 个子目录</template>
            </span>
          </button>
          <div v-if="filled" class="flex shrink-0 rounded border border-zinc-700 p-0.5" role="group" aria-label="空间计算方式">
            <button class="rounded px-2 py-0.5 disabled:cursor-not-allowed disabled:opacity-40" :class="metric === 'allocated' ? 'bg-emerald-400 text-zinc-950' : 'text-zinc-400 hover:text-white'" :disabled="!allocatedKnown" @click="metric = 'allocated'">实际占用</button>
            <button class="rounded px-2 py-0.5" :class="metric === 'logical' ? 'bg-emerald-400 text-zinc-950' : 'text-zinc-400 hover:text-white'" @click="metric = 'logical'">逻辑大小</button>
          </div>
          <div v-else class="flex shrink-0 rounded border border-zinc-700 p-0.5 text-xs" role="group" aria-label="空间计算方式">
            <button class="rounded px-2 py-1 disabled:cursor-not-allowed disabled:opacity-40" :class="metric === 'allocated' ? 'bg-emerald-400 text-zinc-950' : 'text-zinc-400 hover:text-white'" :disabled="!allocatedKnown" @click="metric = 'allocated'">实际占用</button>
            <button class="rounded px-2 py-1" :class="metric === 'logical' ? 'bg-emerald-400 text-zinc-950' : 'text-zinc-400 hover:text-white'" @click="metric = 'logical'">逻辑大小</button>
          </div>
        </div>
        <SpaceMap
          v-show="!mapCollapsed"
          class="mt-1"
          :limit="mapLimit"
          :items="directories"
          :max-size="maxSize"
          :metric="metric"
          :allocated-known="allocatedKnown"
          @open="id => loadDirectory(id)"
        />

        <div class="flex shrink-0 items-center text-xs text-zinc-500" :class="filled ? 'mt-2' : 'mt-3'">
          <span>文件列表{{ loading ? ' · 正在读取…' : '' }}</span>
        </div>
        <EntryList
          class="mt-1"
          :fill="filled"
          :reset-key="`${directory}:${offset}:${sort}`"
          :items="children"
          :loading="loading"
          :metric="metric"
          :allocated-known="allocatedKnown"
          @open="id => loadDirectory(id)"
          @preview="showPreview"
          @error="error = $event"
        />

        <footer class="flex min-h-8 shrink-0 items-center justify-between border-t border-zinc-800 text-xs text-zinc-500" :class="filled ? 'mt-2 pt-2' : 'mt-4 min-h-9 pt-4'">
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

      <!-- 右栏在「列表吃满」时自己滚动 -->
      <aside class="grid min-w-0 max-w-full content-start gap-5" :class="filled ? 'max-h-[calc(100dvh-10rem)] min-h-0 overflow-x-hidden overflow-y-auto' : ''">
        <PanelCard id="current" eyebrow="CURRENT DIRECTORY" title="当前目录最大文件" hint="当前目录直属文件里占用最大的几个">
          <div v-if="overview" class="grid gap-2 text-xs text-zinc-400">
            <div class="flex min-w-0 justify-between gap-3"><span class="shrink-0">路径</span><span class="min-w-0 truncate text-right text-zinc-300" :title="overview.dir.path">{{ overview.dir.path }}</span></div>
            <div class="flex min-w-0 justify-between gap-3"><span class="shrink-0">递归大小</span><span class="min-w-0 text-zinc-300">{{ formatBytes(overview.recursiveSize) }}</span></div>
            <div class="flex min-w-0 justify-between gap-3">
              <span class="shrink-0">递归条目</span>
              <span v-if="overview.countsMissing" class="text-amber-300" title="这两个字段是后加的，旧快照里没有记录，需要重新扫描才有值">需重新扫描</span>
              <span v-else class="text-zinc-300">{{ formatCount(overview.dir.file_count || 0) }} 文件 · {{ formatCount(overview.dir.dir_count || 0) }} 目录</span>
            </div>
            <div class="flex min-w-0 justify-between gap-3"><span class="shrink-0">本层条目</span><span class="min-w-0 text-right text-zinc-300">{{ formatCount(total) }}（{{ overview.pageDirs }} 目录 · {{ overview.pageFiles }} 文件）</span></div>
          </div>

          <div class="my-3 border-t border-zinc-800" />

          <div v-if="!topFiles.length" class="py-1 text-xs text-zinc-500">当前目录没有直属文件</div>
          <div v-else class="grid gap-3">
            <div v-for="(item, index) in topFiles" :key="item.id" class="grid gap-1">
              <div class="flex min-w-0 items-baseline justify-between gap-2 text-xs">
                <span class="min-w-0 truncate text-zinc-300" :title="item.path || item.name">{{ index + 1 }}. {{ item.name }}</span>
                <strong class="shrink-0 font-normal text-zinc-500">{{ formatBytes(itemSize(item)) }}</strong>
              </div>
              <span class="h-2 overflow-hidden rounded bg-zinc-800"><i class="block h-full bg-emerald-400" :style="{ width: `${Math.max(2, (itemSize(item) / topFilesMax) * 100)}%` }" /></span>
            </div>
          </div>
        </PanelCard>

        <PanelCard id="extensions" eyebrow="EXTENSIONS" title="全盘类型占用" hint="整个快照的统计，与当前所在目录无关">
          <div class="mb-3 flex justify-end">
            <div class="flex rounded border border-zinc-700 p-0.5 text-xs" role="group" aria-label="类型占用排序方式">
              <button class="rounded px-2 py-1" :class="!extensionsByCount ? 'bg-emerald-400 text-zinc-950' : 'text-zinc-400 hover:text-white'" @click="extensionsByCount = false">按大小</button>
              <button class="rounded px-2 py-1" :class="extensionsByCount ? 'bg-emerald-400 text-zinc-950' : 'text-zinc-400 hover:text-white'" @click="extensionsByCount = true">按数量</button>
            </div>
          </div>
          <div class="grid gap-3">
            <div v-for="item in extensions" :key="item.extension" class="grid min-w-0 grid-cols-[70px_minmax(0,1fr)_auto] items-center gap-2 text-xs">
              <span class="truncate text-zinc-400">{{ item.extension }}</span>
              <span class="h-2 overflow-hidden rounded bg-zinc-800"><i class="block h-full bg-emerald-400" :style="{ width: `${Math.min(100, (extensionValue(item) / extensionBasis) * 100)}%` }" /></span>
              <strong class="font-normal text-zinc-500">{{ extensionText(item) }}</strong>
            </div>
          </div>
        </PanelCard>
      </aside>
    </section>
    <p v-if="error" class="mt-4 text-sm text-rose-300">{{ error }}</p>
  </main>
  <main v-else class="mx-auto max-w-[1480px] px-5 py-8 text-zinc-500 lg:px-8">正在加载快照...</main>

  <PreviewDrawer :item="previewItem" :metric="metric" :allocated-known="allocatedKnown" @close="previewItem = null" @error="error = $event" />
</template>
