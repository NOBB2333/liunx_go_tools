#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
DIST_DIR="${ROOT_DIR}/dist"
APP_NAME="golangtools"

cd "${ROOT_DIR}"
rm -rf "${DIST_DIR}"
mkdir -p "${DIST_DIR}"

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "缺少构建依赖: $1" >&2
    exit 1
  fi
}

require_command go
require_command pnpm

echo "构建 Vue 前端"
(
  cd "${ROOT_DIR}/web"
  pnpm install --frozen-lockfile
  pnpm run typecheck
  pnpm run build
)

echo "验证 Go 工程"
(
  cd "${ROOT_DIR}"
  go test ./...
  go vet ./...
)

build_one() {
  local goos="$1"
  local goarch="$2"
  local output_name="${APP_NAME}-${goos}-${goarch}"

  if [[ "${goos}" == "windows" ]]; then
    output_name="${output_name}.exe"
  fi

  echo "构建 ${goos}/${goarch} -> ${output_name}"

  # 当前机器 OS/arch 与目标一致时，允许 CGO（macOS 内存扫描需要 CGO）
  local host_os host_arch
  host_os="$(go env GOHOSTOS)"
  host_arch="$(go env GOHOSTARCH)"

  local cgo_flag=0
  if [[ "${goos}" == "${host_os}" && "${goarch}" == "${host_arch}" ]]; then
    cgo_flag=1
  fi

  # 注意：-o 必须用相对路径。Git Bash 下 DIST_DIR 是 /d/xxx 形式，
  # Windows 版 go.exe 会把它解释成「当前盘符根目录」下的路径，产物会写飞。
  GOOS="${goos}" GOARCH="${goarch}" CGO_ENABLED="${cgo_flag}" \
    go build -trimpath -ldflags="-s -w" -o "dist/${output_name}" .

  # native darwin 构建：用 ad-hoc 签名注入 debugger entitlement，使 task_for_pid 可用
  if [[ "${goos}" == "${host_os}" && "${goarch}" == "${host_arch}" && "${goos}" == "darwin" ]]; then
    codesign -s - --entitlements "${ROOT_DIR}/debugger.entitlements.plist" --force "dist/${output_name}" 2>&1 || true
  fi
}

build_one darwin amd64
build_one darwin arm64
build_one linux amd64
build_one linux arm64
build_one windows amd64
build_one windows arm64

cp "${ROOT_DIR}/README.md" "${DIST_DIR}/README.md"
cp "${ROOT_DIR}/build.sh" "${ROOT_DIR}/build.ps1" "${ROOT_DIR}/build.cmd" "${DIST_DIR}/"
mkdir -p "${DIST_DIR}/docs"
cp "${ROOT_DIR}"/docs/*.md "${DIST_DIR}/docs/"

if [[ -d "${ROOT_DIR}/Handle" ]]; then
  mkdir -p "${DIST_DIR}/Handle"
  cp -R "${ROOT_DIR}/Handle/." "${DIST_DIR}/Handle/"
fi

ARTIFACTS=(
  golangtools-darwin-amd64
  golangtools-darwin-arm64
  golangtools-linux-amd64
  golangtools-linux-arm64
  golangtools-windows-amd64.exe
  golangtools-windows-arm64.exe
)

# 先校验产物齐全，避免"构建成功但文件不在"这类问题被一路带到最后
for artifact in "${ARTIFACTS[@]}"; do
  if [[ ! -f "${DIST_DIR}/${artifact}" ]]; then
    echo "构建产物缺失: ${DIST_DIR}/${artifact}" >&2
    exit 1
  fi
done

(
  cd "${DIST_DIR}"
  # 优先用 sha256sum（Git Bash/精简 Linux 无 shasum）
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${ARTIFACTS[@]}" > SHA256SUMS
  else
    shasum -a 256 "${ARTIFACTS[@]}" > SHA256SUMS
  fi
)

find "${DIST_DIR}" -name .DS_Store -type f -delete

echo "完成，输出目录: ${DIST_DIR}"
