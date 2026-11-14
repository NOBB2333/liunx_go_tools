#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "Usage: $0 <version-tag> <binary-directory> <output-directory>" >&2
  exit 2
fi

version="$1"
if [[ ! "$version" =~ ^[vV][0-9][A-Za-z0-9._+-]*$ ]]; then
  echo "Invalid version tag: $version" >&2
  exit 2
fi

root_dir="$(cd "$(dirname "$0")/.." && pwd)"
bin_dir="$(cd "$2" && pwd)"
mkdir -p "$3"
output_dir="$(cd "$3" && pwd)"
targets=(darwin-amd64 darwin-arm64 linux-amd64 linux-arm64 windows-amd64 windows-arm64)
archives=()

# Check every required input and output before producing any archive.
for target in "${targets[@]}"; do
  binary="golangtools-$target"
  ext=tar.gz
  if [[ "$target" == windows-* ]]; then
    binary+='.exe'
    ext=zip
  fi
  archive="golangtools-$version-$target.$ext"
  if [[ ! -f "$bin_dir/$binary" ]]; then
    echo "Missing release binary: $bin_dir/$binary" >&2
    exit 1
  fi
  if [[ -e "$output_dir/$archive" ]]; then
    echo "Release archive already exists: $output_dir/$archive" >&2
    exit 1
  fi
  archives+=("$archive")
done
if [[ -e "$output_dir/SHA256SUMS" ]]; then
  echo "Checksum file already exists: $output_dir/SHA256SUMS" >&2
  exit 1
fi

work_dir="$(mktemp -d "${TMPDIR:-/tmp}/golangtools-release.XXXXXXXX")"
trap 'rm -rf "$work_dir"' EXIT
export COPYFILE_DISABLE=1

for index in "${!targets[@]}"; do
  target="${targets[$index]}"
  binary="golangtools-$target"
  if [[ "$target" == windows-* ]]; then binary+='.exe'; fi
  stage="$work_dir/$target"
  mkdir -p "$stage/docs"
  cp "$bin_dir/$binary" "$stage/$binary"
  chmod 755 "$stage/$binary"
  cp "$root_dir/README.md" "$stage/README.md"
  cp "$root_dir"/docs/*.md "$stage/docs/"
  if [[ "$target" == windows-* ]]; then
    (cd "$stage" && zip -qr "$output_dir/${archives[$index]}" .)
  else
    tar -czf "$output_dir/${archives[$index]}" -C "$stage" .
  fi
done

(cd "$output_dir" && shasum -a 256 "${archives[@]}" > SHA256SUMS)
printf 'Created %s archives and SHA256SUMS in %s\n' "${#archives[@]}" "$output_dir"
