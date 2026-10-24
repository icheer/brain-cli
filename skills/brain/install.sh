#!/usr/bin/env bash
# brain 安装器：从 GitHub Release 拉取当前平台的二进制到本技能目录（SHA256 校验）。
# 用法：bash install.sh   （SKILL.md 首次使用前引导执行；BRAIN_INSTALL_DIR 可改安装位置）
# 设计说明：Go + TLS 的二进制下限约 6.4MB，无法塞进技能目录（≤1MiB 预期），
# 故技能只携带本安装器，二进制按需下载。
set -euo pipefail

REPO="icheer/brain-cli"
DIR="$(cd "$(dirname "$0")" && pwd)"
TARGET="${BRAIN_INSTALL_DIR:-$DIR/brain-bin}"

os_name() {
  case "$(uname -s)" in
    Darwin) echo darwin ;;
    Linux) echo linux ;;
    MINGW* | MSYS* | CYGWIN*) echo windows ;;
    *) echo "不支持的系统: $(uname -s)" >&2; return 1 ;;
  esac
}

arch_name() {
  case "$(uname -m)" in
    x86_64 | amd64) echo amd64 ;;
    aarch64 | arm64) echo arm64 ;;
    *) echo "不支持的架构: $(uname -m)" >&2; return 1 ;;
  esac
}

OS="$(os_name)"
ARCH="$(arch_name)"
VER="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" |
  grep -o '"tag_name": *"[^"]*"' | head -1 | sed 's/.*"v/v/;s/"$//')"
[ -n "$VER" ] || { echo "获取最新版本失败（检查网络/仓库 ${REPO}）" >&2; exit 1; }

BASE="brain_${VER#v}_${OS}_${ARCH}"
URL="https://github.com/${REPO}/releases/download/${VER}/${BASE}.tar.gz"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "下载 brain ${VER} (${OS}/${ARCH}) → ${TARGET}/brain"
curl -fsSL "$URL" -o "$TMP/${BASE}.tar.gz"
curl -fsSL "https://github.com/${REPO}/releases/download/${VER}/SHA256SUMS" -o "$TMP/SHA256SUMS"

# 校验压缩包哈希（对应 goreleaser 产出的 SHA256SUMS）
WANT="$(grep " ${BASE}.tar.gz\$" "$TMP/SHA256SUMS" | awk '{print $1}')"
if [ -n "$WANT" ]; then
  echo "${WANT}  ${TMP}/${BASE}.tar.gz" | sha256sum -c - >/dev/null || { echo "SHA256 校验失败" >&2; exit 1; }
else
  echo "警告: SHA256SUMS 中未找到 ${BASE}.tar.gz，跳过校验" >&2
fi

mkdir -p "$TARGET"
tar -xzf "$TMP/${BASE}.tar.gz" -C "$TARGET" brain
chmod +x "$TARGET/brain"
"$TARGET/brain" --version
echo "完成。调用方式：${TARGET}/brain ask -m fast \"...\"（或把 ${TARGET} 加入 PATH）"
