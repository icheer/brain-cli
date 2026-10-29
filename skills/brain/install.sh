#!/usr/bin/env bash
# brain 安装器：从 GitHub Release 拉取当前平台的二进制（SHA256 校验）。
# 用法：bash install.sh [--global | --link]（详见 --help）
#   (无参数)   首次装到本技能目录 bin/ 下；已有安装时自动检测既有形态
#             （全局软链 → --link；全局二进制 → --global；两处皆有 → 都更新；
#              仅技能目录 → 就地升级）按同模式升级
#   --global  安装到全局二进制目录（默认 /usr/local/bin，Windows Git Bash 为 ~/bin，
#             BRAIN_GLOBAL_BIN 可改）
#   --link    安装到本技能目录 bin/ 下，并在全局目录建软链接指向它（单一二进制源：
#             此后重跑本脚本升级技能即全局生效）
# 升级时包内 SKILL.md（v0.10.3 起随 Release 发布）会同步覆盖技能目录副本，
# 保证文档与二进制同版本出厂。
# 设计说明：Go + TLS 的二进制下限约 6.4MB，无法塞进技能目录（≤1MiB 预期），
# 故技能只携带本安装器，二进制按需下载。
# Windows：Git Bash / MSYS / Cygwin 下运行（goreleaser 的 windows 产物为 brain.exe，
# 全程按平台带 .exe 后缀；WSL 下 uname 为 Linux，走常规 Unix 路径）。
set -euo pipefail

REPO="icheer/brain-cli"
DIR="$(cd "$(dirname "$0")" && pwd)"

usage() {
  cat <<'EOF'
用法: bash install.sh [--global | --link]
  (无参数)   首次装到本技能目录 bin/ 下；已有安装时自动检测既有形态按同模式升级
            （全局软链=--link；全局二进制=--global；两处皆有=都更新；仅技能目录=就地升级）
  --global  安装到全局二进制目录（默认 /usr/local/bin；不可写时自动 sudo）
  --link    安装到本技能目录 bin/ 下并在全局目录建软链接
            （单一二进制源：此后重跑 install.sh 升级技能即全局生效；
             Windows Git Bash 软链接不可靠，退化为复制）
升级说明: v0.10.3 起重跑 install.sh 会同步包内 SKILL.md（技能文档随 Release 发布，
          与二进制同版本出厂）；旧版本安装器无此逻辑，存量安装需手动整目录复制一次
环境变量: BRAIN_INSTALL_DIR 覆盖技能目录安装位置；BRAIN_GLOBAL_BIN 覆盖全局目录
          （默认 Unix /usr/local/bin，Windows Git Bash ~/bin——Git Bash 在其
            存在时自动加入 PATH，无需管理员）
Windows:  在 Git Bash 或 WSL 下运行；Git Bash 产物为 brain.exe，WSL 走 Linux 路径
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

# goreleaser 对 windows 产物恒追加 .exe（Release 包内成员即 brain.exe）
EXT=""
if [ "$OS" = "windows" ]; then
  EXT=".exe"
fi

VER="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" |
  grep -o '"tag_name": *"[^"]*"' | head -1 | sed 's/.*"v/v/;s/"$//')"
[ -n "$VER" ] || { echo "获取最新版本失败（检查网络/仓库 ${REPO}）" >&2; exit 1; }

BASE="brain_${VER#v}_${OS}_${ARCH}"
URL="https://github.com/${REPO}/releases/download/${VER}/${BASE}.tar.gz"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# 全局目录默认值：Unix /usr/local/bin；Windows Git Bash 的 /usr/local 位于
# Git 安装目录（Program Files）下，通常不可写且无 sudo——改用 $HOME/bin
# （Git Bash 的 /etc/profile 在 ~/bin 存在时自动将其加入 PATH）。
if [ -n "${BRAIN_GLOBAL_BIN:-}" ]; then
  GLOBAL_BIN="${BRAIN_GLOBAL_BIN}"
elif [ "$OS" = "windows" ]; then
  GLOBAL_BIN="${HOME}/bin"
else
  GLOBAL_BIN="/usr/local/bin"
fi

# 智能升级检测（仅无参数调用时；显式 --global/--link 精确执行不检测）：
# 按既有安装形态自动选择模式。软链优先（最特异）；Git Bash 的 --link 退化形态
# （全局+技能目录各一份副本）归入 both 一并更新。
if [ -z "$MODE_FLAGS" ]; then
  GBIN="${GLOBAL_BIN}/brain${EXT}"
  LBIN="${BRAIN_INSTALL_DIR:-$DIR/bin}/brain${EXT}"
  if [ -L "$GBIN" ]; then
    MODE="link"
    echo "检测到全局软链（--link 形态），按同模式升级"
  elif [ -e "$GBIN" ] && [ -e "$LBIN" ]; then
    MODE="both"
    echo "检测到全局与技能目录各有一份二进制（Git Bash 的 --link 复制形态或双重安装），两处均更新"
  elif [ -e "$GBIN" ]; then
    MODE="global"
    echo "检测到全局二进制（--global 形态），按同模式升级"
  elif [ -e "$LBIN" ]; then
    echo "检测到技能目录二进制，就地升级"
  fi
fi

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
tar -xzf "$TMP/${BASE}.tar.gz" -C "$TMP" "brain${EXT}"
# v0.10.3 起包内含技能文档（goreleaser 按仓库相对路径打包，成员名带 skills/brain/ 前缀）；
# 对旧版本包/布局变化容错提取，之后按实际存在位置取用
SKILL_SRC=""
for cand in "skills/brain/SKILL.md" "SKILL.md"; do
  if tar -xzf "$TMP/${BASE}.tar.gz" -C "$TMP" "$cand" 2>/dev/null; then
    if [ -f "$TMP/$cand" ]; then SKILL_SRC="$TMP/$cand"; break; fi
  fi
done

# ensure_global_dir 确保全局目录存在：直建，不行借 sudo，再不行明确指引退出。
ensure_global_dir() {
  if [ -d "$GLOBAL_BIN" ]; then return 0; fi
  if mkdir -p "$GLOBAL_BIN" 2>/dev/null; then return 0; fi
  if command -v sudo >/dev/null 2>&1; then
    sudo mkdir -p "$GLOBAL_BIN"
    return 0
  fi
  echo "错误: 无法创建 ${GLOBAL_BIN}（无权限且系统无 sudo）；设 BRAIN_GLOBAL_BIN=<可写目录>（如 ~/.local/bin）后重试" >&2
  return 1
}

# put_global 把二进制放进全局目录：可写直写，否则借 sudo，再否则明确指引。
# 先 rm 再写，避免目标是既有软链接时 cp 穿透链接写错位置。
put_global() { # $1 = 源二进制
  ensure_global_dir
  if [ -w "$GLOBAL_BIN" ]; then
    rm -f "$GLOBAL_BIN/brain${EXT}"
    cp "$1" "$GLOBAL_BIN/brain${EXT}"
    chmod 0755 "$GLOBAL_BIN/brain${EXT}"
  elif command -v sudo >/dev/null 2>&1; then
    sudo rm -f "$GLOBAL_BIN/brain${EXT}"
    sudo cp "$1" "$GLOBAL_BIN/brain${EXT}"
    sudo chmod 0755 "$GLOBAL_BIN/brain${EXT}"
  else
    echo "错误: ${GLOBAL_BIN} 不可写且系统无 sudo；设 BRAIN_GLOBAL_BIN=<可写目录>（如 ~/.local/bin）后重试" >&2
    exit 1
  fi
}

# link_global 在全局目录建软链接（权限策略与 put_global 一致）
link_global() { # $1 = 软链接目标（技能目录里的二进制）
  ensure_global_dir
  if [ -w "$GLOBAL_BIN" ]; then
    ln -sfn "$1" "$GLOBAL_BIN/brain${EXT}"
  elif command -v sudo >/dev/null 2>&1; then
    sudo ln -sfn "$1" "$GLOBAL_BIN/brain${EXT}"
  else
    echo "错误: ${GLOBAL_BIN} 不可写且系统无 sudo；设 BRAIN_GLOBAL_BIN=<可写目录>（如 ~/.local/bin）后重试" >&2
    exit 1
  fi
}

case "$MODE" in
  local | link | both)
    TARGET="${BRAIN_INSTALL_DIR:-$DIR/bin}"
    mkdir -p "$TARGET"
    cp "$TMP/brain${EXT}" "$TARGET/brain${EXT}"
    chmod +x "$TARGET/brain${EXT}"
    ;;
  global)
    TARGET="$GLOBAL_BIN"
    put_global "$TMP/brain${EXT}"
    ;;
esac

if [ "$MODE" = "both" ]; then
  put_global "$TARGET/brain${EXT}"
fi

if [ "$MODE" = "link" ]; then
  if [ "$OS" = "windows" ]; then
    echo "Windows Git Bash 软链接不可靠，--link 退化为复制到 ${GLOBAL_BIN}" >&2
    put_global "$TARGET/brain${EXT}"
  else
    link_global "$TARGET/brain${EXT}"
  fi
fi

# 同步技能文档：包内 SKILL.md 与二进制同版本出厂，覆盖技能目录里的旧副本
# （仅当脚本确实位于含 SKILL.md 的技能目录时；内容相同则静默跳过）
if [ -n "$SKILL_SRC" ] && [ -f "$DIR/SKILL.md" ]; then
  if ! cmp -s "$SKILL_SRC" "$DIR/SKILL.md"; then
    cp "$SKILL_SRC" "$DIR/SKILL.md"
    echo "SKILL.md 已同步至 ${VER}"
  fi
fi

# 校验安装产物（--global 校验全局目录里的那份）
if [ "$MODE" = "global" ]; then
  "$GLOBAL_BIN/brain${EXT}" --version
else
  "$TARGET/brain${EXT}" --version
fi

if [ "$MODE" != "local" ]; then
  case ":$PATH:" in
    *":$GLOBAL_BIN:"*) ;;
    *) echo "提示: ${GLOBAL_BIN} 不在当前 PATH 中（新开终端/自行加入后全局生效）" >&2 ;;
  esac
fi

case "$MODE" in
  local)
    echo "完成。调用方式：${TARGET}/brain${EXT} ask -m fast \"...\"（或把 ${TARGET} 加入 PATH；终端全局安装可加 --link）"
    ;;
  global)
    echo "完成。全局可用：brain ask -m fast \"...\""
    ;;
  both)
    echo "完成。技能目录: ${TARGET}/brain${EXT}；全局: ${GLOBAL_BIN}/brain${EXT}（两处均已更新；Windows Git Bash 下即 --link 的复制形态）"
    ;;
  link)
    echo "完成。技能目录: ${TARGET}/brain${EXT}；全局: ${GLOBAL_BIN}/brain${EXT} → ${TARGET}/brain${EXT}（重跑 install.sh 升级即全局生效；Windows Git Bash 下为复制副本）"
    ;;
esac
