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

  GOOS="${goos}" GOARCH="${goarch}" CGO_ENABLED="${cgo_flag}" \
    go build -trimpath -ldflags="-s -w" -o "${DIST_DIR}/${output_name}" .

  # native darwin 构建：用 ad-hoc 签名注入 debugger entitlement，使 task_for_pid 可用
  if [[ "${goos}" == "${host_os}" && "${goarch}" == "${host_arch}" && "${goos}" == "darwin" ]]; then
    codesign -s - --entitlements "${ROOT_DIR}/debugger.entitlements.plist" --force "${DIST_DIR}/${output_name}" 2>&1 || true
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

(
  cd "${DIST_DIR}"
  shasum -a 256 \
    golangtools-darwin-amd64 \
    golangtools-darwin-arm64 \
    golangtools-linux-amd64 \
    golangtools-linux-arm64 \
    golangtools-windows-amd64.exe \
    golangtools-windows-arm64.exe > SHA256SUMS
)

find "${DIST_DIR}" -name .DS_Store -type f -delete

echo "完成，输出目录: ${DIST_DIR}"
