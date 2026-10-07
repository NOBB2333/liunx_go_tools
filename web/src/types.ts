export type Manifest = {
  root: string
  scanner_backend?: string
  allocated_bytes_known: boolean
  allocation_source?: string
  files: number
  directories: number
  logical_bytes: number
  allocated_bytes: number
  duration_ns: number
  entries_per_second: number
  errors: number
  complete: boolean
}

export type Item = {
  id: number
  parent_id: number
  name: string
  is_dir: boolean
  size_bytes: number
  allocated_bytes: number
  file_count?: number
  dir_count?: number
  mtime_ns?: number
  ctime_ns?: number
  birthtime_ns?: number
  extension?: string
  path?: string
}

export type DirectoryInfo = {
  id: number
  parent_id: number
  name: string
  depth: number
  path?: string
  // 递归统计值。在这些字段被写入索引之前生成的旧快照不会有它们，
  // 所以 0 要当作「不可用」处理，而不是当作空目录。
  size_bytes?: number
  allocated_bytes?: number
  file_count?: number
  dir_count?: number
}

export type DirectoryContext = {
  directory: DirectoryInfo
  breadcrumbs: DirectoryInfo[]
}

export type PageMeta = {
  total?: number
  limit: number
  offset: number
}

export type Page<T> = {
  items: T[]
  meta: PageMeta
}

export type Summary = {
  manifest: Manifest
  indexed_files: number
  indexed_directories: number
  snapshot_file?: string
  path_root?: string
}

export type ExtensionStat = {
  extension: string
  files: number
  size_bytes: number
  allocated_bytes: number
}

export type UiPrefs = {
  /** 各个可折叠区块的收起状态，键是区块 id */
  collapsed: Record<string, boolean>
}

export type ScanError = {
  path: string
  error: string
}
