#!/usr/bin/env bash
# brain 安装器：从 GitHub Release 拉取当前平台的二进制（SHA256 校验）。
# 用法：bash install.sh [--global | --link]（详见 --help）
#   (无参数)   安装到本技能目录 bin/ 下（默认；SKILL.md 首次使用前引导执行）
#   --global  安装到全局二进制目录（默认 /usr/local/bin，BRAIN_GLOBAL_BIN 可改）
#   --link    安装到本技能目录 bin/，并在全局目录建软链接指向它（单一二进制源：
#             此后重跑本脚本升级技能即全局生效）
# 设计说明：Go + TLS 的二进制下限约 6.4MB，无法塞进技能目录（≤1MiB 预期），
# 故技能只携带本安装器，二进制按需下载。
set -euo pipefail

REPO="icheer/brain-cli"
DIR="$(cd "$(dirname "$0")" && pwd)"

usage() {
  cat <<'EOF'
用法: bash install.sh [--global | --link]
  (无参数)   安装到本技能目录 bin/ 下（默认）
  --global  安装到全局二进制目录（默认 /usr/local/bin；不可写时自动 sudo）
  --link    安装到本技能目录 bin/ 并在全局目录建软链接指向它
            （单一二进制源：此后重跑 install.sh 升级技能即全局生效；
             Windows Git Bash 软链接不可靠，退化为复制）
环境变量: BRAIN_INSTALL_DIR 覆盖技能目录安装位置；BRAIN_GLOBAL_BIN 覆盖全局目录
EOF
}

MODE="local"
MODE_FLAGS=""
while [ $# -gt 0 ]; do
  case "$1" in
    --global|--link)
      [ -z "$MODE_FLAGS" ] || { echo "错误: --global 与 --link 互斥" >&2; exit 1; }
      MODE_FLAGS="$1"
      MODE="${1#--}"
      ;;
    -h|--help)
      usage; exit 0
      ;;
    *)
      echo "未知参数: $1（--help 查看用法）" >&2
      exit 1
      ;;
  esac
  shift
done

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

GLOBAL_BIN="${BRAIN_GLOBAL_BIN:-/usr/local/bin}"

echo "下载 brain ${VER} (${OS}/${ARCH})"
curl -fsSL "$URL" -o "$TMP/${BASE}.tar.gz"
curl -fsSL "https://github.com/${REPO}/releases/download/${VER}/SHA256SUMS" -o "$TMP/SHA256SUMS"

# 校验压缩包哈希（对应 goreleaser 产出的 SHA256SUMS）
WANT="$(grep " ${BASE}.tar.gz\$" "$TMP/SHA256SUMS" | awk '{print $1}')"
if [ -n "$WANT" ]; then
  echo "${WANT}  ${TMP}/${BASE}.tar.gz" | sha256sum -c - >/dev/null || { echo "SHA256 校验失败" >&2; exit 1; }
else
  echo "警告: SHA256SUMS 中未找到 ${BASE}.tar.gz，跳过校验" >&2
fi
tar -xzf "$TMP/${BASE}.tar.gz" -C "$TMP" brain

# put_global 把二进制放进全局目录：可写直写，否则借 sudo，再否则明确指引。
# 先 rm 再写，避免目标是既有软链接时 cp 穿透链接写错位置。
put_global() { # $1 = 源二进制
  if [ ! -d "$GLOBAL_BIN" ]; then
    mkdir -p "$GLOBAL_BIN" 2>/dev/null || sudo mkdir -p "$GLOBAL_BIN"
  fi
  if [ -w "$GLOBAL_BIN" ]; then
    rm -f "$GLOBAL_BIN/brain"
    cp "$1" "$GLOBAL_BIN/brain"
    chmod 0755 "$GLOBAL_BIN/brain"
  elif command -v sudo >/dev/null 2>&1; then
    sudo rm -f "$GLOBAL_BIN/brain"
    sudo cp "$1" "$GLOBAL_BIN/brain"
    sudo chmod 0755 "$GLOBAL_BIN/brain"
  else
    echo "错误: ${GLOBAL_BIN} 不可写且系统无 sudo；设 BRAIN_GLOBAL_BIN=<可写目录>（如 ~/.local/bin）后重试" >&2
    exit 1
  fi
}

# link_global 在全局目录建软链接（权限策略与 put_global 一致）
link_global() { # $1 = 软链接目标（技能目录里的二进制）
  if [ ! -d "$GLOBAL_BIN" ]; then
    mkdir -p "$GLOBAL_BIN" 2>/dev/null || sudo mkdir -p "$GLOBAL_BIN"
  fi
  if [ -w "$GLOBAL_BIN" ]; then
    ln -sfn "$1" "$GLOBAL_BIN/brain"
  elif command -v sudo >/dev/null 2>&1; then
    sudo ln -sfn "$1" "$GLOBAL_BIN/brain"
  else
    echo "错误: ${GLOBAL_BIN} 不可写且系统无 sudo；设 BRAIN_GLOBAL_BIN=<可写目录>（如 ~/.local/bin）后重试" >&2
    exit 1
  fi
}

case "$MODE" in
  local | link)
    TARGET="${BRAIN_INSTALL_DIR:-$DIR/bin}"
    mkdir -p "$TARGET"
    cp "$TMP/brain" "$TARGET/brain"
    chmod +x "$TARGET/brain"
    ;;
  global)
    TARGET="$GLOBAL_BIN"
    put_global "$TMP/brain"
    ;;
esac

if [ "$MODE" = "link" ]; then
  if [ "$OS" = "windows" ]; then
    echo "Windows Git Bash 软链接不可靠，--link 退化为复制到 ${GLOBAL_BIN}" >&2
    put_global "$TARGET/brain"
  else
    link_global "$TARGET/brain"
  fi
fi

# 校验安装产物（--global 校验全局目录里的那份）
if [ "$MODE" = "global" ]; then
  "$GLOBAL_BIN/brain" --version
else
  "$TARGET/brain" --version
fi

if [ "$MODE" != "local" ]; then
  case ":$PATH:" in
    *":$GLOBAL_BIN:"*) ;;
    *) echo "提示: ${GLOBAL_BIN} 不在当前 PATH 中，需自行加入" >&2 ;;
  esac
fi

case "$MODE" in
  local)
    echo "完成。调用方式：${TARGET}/brain ask -m fast \"...\"（或把 ${TARGET} 加入 PATH；终端全局安装可加 --link）"
    ;;
  global)
    echo "完成。全局可用：brain ask -m fast \"...\""
    ;;
  link)
    echo "完成。技能目录: ${TARGET}/brain；全局软链: ${GLOBAL_BIN}/brain → ${TARGET}/brain（重跑 install.sh 升级即全局生效）"
    ;;
esac
