import { reactive, watch } from 'vue'
import type { UiPrefs } from '../types'

const STORAGE_KEY = 'golangtools.filesystem.ui'

function readPrefs(): UiPrefs {
  const fallback: UiPrefs = { collapsed: {} }
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY)
    if (!raw) return fallback
    const parsed = JSON.parse(raw) as Partial<UiPrefs> | null
    if (!parsed || typeof parsed !== 'object') return fallback
    const collapsed = parsed.collapsed && typeof parsed.collapsed === 'object' ? { ...parsed.collapsed } : {}
    return { collapsed }
  } catch {
    // 隐私模式下访问 localStorage 会抛异常。布局偏好只是便利功能，不是必需项，
    // 因此直接退回默认值。
    return fallback
  }
}

// 模块级单例：所有面板读写同一份偏好；初始读取是同步的，
// 这样首帧就直接是保存过的布局，不会先闪一下默认布局。
const prefs = reactive<UiPrefs>(readPrefs())

watch(prefs, value => {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(value))
  } catch {
    // 偏好持久化是尽力而为，失败不影响使用。
  }
}, { deep: true })

export function useUiPrefs() {
  return {
    prefs,
    isCollapsed: (id: string) => prefs.collapsed[id] === true,
    toggleCollapsed: (id: string) => { prefs.collapsed[id] = !prefs.collapsed[id] },
    setCollapsed: (id: string, value: boolean) => { prefs.collapsed[id] = value },
  }
}
