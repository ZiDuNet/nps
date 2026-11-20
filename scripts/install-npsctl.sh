#!/usr/bin/env bash
# Install the npsctl CLI for NPS Pro.
# Downloads the matching release binary and falls back to mirrors when GitHub
# is unreachable. Usage:
#   curl -fsSL https://raw.githubusercontent.com/ZiDuNet/nps/master/scripts/install-npsctl.sh | bash
set -euo pipefail

REPO="ZiDuNet/nps"
VERSION="latest"
BIN_NAME="npsctl"
INSTALL_DIR="${NPSCTL_INSTALL_DIR:-$HOME/.local/bin}"

detect_platform() {
    local os arch
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"
    case "$arch" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        i386|i686) arch="386" ;;
        armv7l) arch="armv7" ;;
    esac
    case "$os" in
        darwin) os="darwin" ;;
        linux) os="linux" ;;
        mingw*|msys*|cygwin*) os="windows" ;;
    esac
    echo "${os}_${arch}"
}

platform="$(detect_platform)"
asset="npsctl_${platform}"
if [[ "$platform" == windows_* ]]; then asset="${asset}.exe"; fi

urls=(
    "https://github.com/${REPO}/releases/${VERSION}/download/${asset}"
    "https://ghproxy.cn/https://github.com/${REPO}/releases/${VERSION}/download/${asset}"
    "https://mirror.ghproxy.com/https://github.com/${REPO}/releases/${VERSION}/download/${asset}"
)

mkdir -p "$INSTALL_DIR"
tmp="$(mktemp)"
downloaded=0
for url in "${urls[@]}"; do
    if curl -fsSL --connect-timeout 10 --retry 2 "$url" -o "$tmp"; then
        downloaded=1
        break
    fi
done
if [[ "$downloaded" != "1" ]]; then
    echo "下载 npsctl 失败，请手动从 GitHub Releases 下载并放入 PATH。" >&2
    rm -f "$tmp"
    exit 1
fi

chmod +x "$tmp"
if [[ "$platform" == windows_* ]]; then
    mv -f "$tmp" "$INSTALL_DIR/${BIN_NAME}.exe"
else
    mv -f "$tmp" "$INSTALL_DIR/${BIN_NAME}"
fi

case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *) echo "提示：请把 $INSTALL_DIR 加入 PATH，例如在 ~/.bashrc 追加 export PATH=\"\$PATH:$INSTALL_DIR\"" ;;
esac

echo "npsctl 已安装到 $INSTALL_DIR/${BIN_NAME}"
"$INSTALL_DIR/${BIN_NAME}" version || true
