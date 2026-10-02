<script setup lang="ts">
defineProps<{ label: string; value: string }>()
</script>

<template>
  <div class="rounded-lg border border-zinc-800 bg-zinc-900 p-4">
    <span class="text-xs text-zinc-500">{{ label }}</span>
    <strong class="mt-2 block text-xl font-semibold text-zinc-100">{{ value }}</strong>
  </div>
</template>
