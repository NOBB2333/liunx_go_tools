import type { DirectoryContext, ExtensionStat, Item, Page, PageMeta, Summary } from './types'

type Envelope<T> = {
  data: T
  meta: PageMeta | Record<string, never>
  error?: { message?: string }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<Envelope<T>> {
  const response = await fetch(path, {
    ...init,
    headers: { Accept: 'application/json', ...(init.headers || {}) },
  })
  const payload = await response.json() as Envelope<T>
  if (!response.ok || payload.error) throw new Error(payload.error?.message || '请求失败')
  return payload
}

export const getSummary = async () => (await request<Summary>('/api/v1/snapshots/active/summary')).data
export const getExtensions = async () => (await request<ExtensionStat[]>('/api/v1/snapshots/active/extensions?limit=8')).data
export const getDirectory = async (id: number) => (await request<DirectoryContext>(`/api/v1/snapshots/active/directories/${id}`)).data

export async function getChildren(id: number, sort: string, offset: number, limit = 100): Promise<Page<Item>> {
  const payload = await request<Item[]>(`/api/v1/snapshots/active/directories/${id}/children?limit=${limit}&offset=${offset}&sort=${encodeURIComponent(sort)}`)
  return { items: payload.data, meta: payload.meta as PageMeta }
}

export async function searchFiles(query: string, offset: number, limit = 100): Promise<Page<Item>> {
  const payload = await request<Item[]>(`/api/v1/snapshots/active/search?q=${encodeURIComponent(query)}&limit=${limit}&offset=${offset}`)
  return { items: payload.data, meta: payload.meta as PageMeta }
}

export async function openItem(id: number, isDir: boolean): Promise<string> {
  const payload = await request<{ path: string }>('/api/v1/snapshots/active/open', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id, is_dir: isDir }),
  })
  return payload.data.path
}

export function formatBytes(value: number): string {
  if (!Number.isFinite(value)) return '-'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let size = value
  let unit = 0
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024
    unit += 1
  }
  return `${size.toFixed(size >= 100 ? 0 : 1)} ${units[unit]}`
}

export function formatCount(value: number): string {
  return new Intl.NumberFormat('zh-CN').format(value || 0)
}

export function formatTimestamp(ns?: number): string {
  if (!ns) return '不可用'
  const date = new Date(ns / 1e6)
  if (Number.isNaN(date.getTime())) return '不可用'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit',
  }).format(date)
}

export async function copyText(value: string): Promise<boolean> {
  if (!value || !navigator.clipboard) return false
  try {
    await navigator.clipboard.writeText(value)
    return true
  } catch {
    return false
  }
}
