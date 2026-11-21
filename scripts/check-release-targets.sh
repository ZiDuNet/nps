#!/usr/bin/env bash
set -euo pipefail

targets=(
  "linux amd64 "
  "linux 386 "
  "linux arm GOARM=7"
  "linux arm GOARM=6"
  "linux arm GOARM=5"
  "linux arm64 "
  "freebsd 386 "
  "freebsd amd64 "
  "freebsd arm "
  "darwin amd64 "
  "darwin arm64 "
  "windows 386 "
  "windows amd64 "
  "windows arm64 "
)

tmp_dir="$(mktemp -d "$PWD/.release-target-check.XXXXXX")"
mkdir -p "$tmp_dir/gotmp"
export GOTMPDIR="$tmp_dir/gotmp"
trap 'rm -rf "$tmp_dir"' EXIT

if go list -deps ./cmd/npc | grep -Eq '^modernc\.org/(sqlite|libc)$'; then
  echo "NPC unexpectedly imports the server-only SQLite driver" >&2
  exit 1
fi
if ! go list -deps ./cmd/nps | grep -Eq '^modernc\.org/(sqlite|libc)$'; then
  echo "NPS does not import the SQLite driver" >&2
  exit 1
fi

for target in "${targets[@]}"; do
  read -r goos goarch arch_env <<<"$target"
  output_suffix="${goos}_${goarch}"
  if [[ -n "${arch_env:-}" ]]; then
    output_suffix+="_${arch_env#GOARM=}"
    export "$arch_env"
  else
    unset GOARM
  fi

  echo "Checking ${goos}/${goarch}${arch_env:+ ${arch_env}}"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -o "$tmp_dir/nps_$output_suffix" ./cmd/nps
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -o "$tmp_dir/npc_$output_suffix" ./cmd/npc
done

# NPC does not compile the server SQLite driver, so its historical MIPS
# binaries remain available even though SQLite-backed NPS servers do not build
# for those architectures.
for arch in mips mipsle mips64 mips64le; do
  echo "Checking linux/$arch NPC"
  GOMIPS=softfloat GOMIPS64=softfloat CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -trimpath -o "$tmp_dir/npc_linux_$arch" ./cmd/npc
done
