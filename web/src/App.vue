<script setup lang="ts">
import { HardDrive, Search } from 'lucide-vue-next'
import { RouterLink, RouterView } from 'vue-router'
</script>

<template>
  <div class="min-h-screen bg-zinc-950 text-zinc-100">
    <nav class="border-b border-zinc-800 bg-zinc-950/95">
      <div class="mx-auto flex max-w-[1480px] items-center justify-between px-5 py-4 lg:px-8">
        <RouterLink to="/" class="flex min-w-0 items-center gap-3">
          <span class="flex h-8 w-8 shrink-0 items-center justify-center rounded bg-emerald-400 text-zinc-950">
            <HardDrive :size="18" aria-hidden="true" />
          </span>
          <span class="truncate font-semibold">磁盘空间分析</span>
        </RouterLink>
        <div class="flex gap-1 text-sm text-zinc-400">
          <RouterLink to="/" class="flex h-9 items-center gap-2 rounded px-3 hover:bg-zinc-800 hover:text-white" active-class="bg-zinc-800 text-white">
            <HardDrive :size="16" aria-hidden="true" />
            <span>总览</span>
          </RouterLink>
          <RouterLink to="/search" class="flex h-9 items-center gap-2 rounded px-3 hover:bg-zinc-800 hover:text-white" active-class="bg-zinc-800 text-white">
            <Search :size="16" aria-hidden="true" />
            <span>搜索</span>
          </RouterLink>
        </div>
      </div>
    </nav>
    <RouterView />
  </div>
</template>
