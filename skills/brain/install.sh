#!/usr/bin/env bash
# brain 安装器：从 GitHub Release 拉取当前平台的二进制（SHA256 校验）。
# 用法：bash install.sh [--global | --link | --uninstall]（详见 --help）
#   (无参数)   首次装到本技能目录 bin/ 下；已有安装时自动检测既有形态
#             （全局软链 → --link；全局二进制 → --global；两处皆有 → 都更新；
#              仅技能目录 → 就地升级）按同模式升级
#   --global  安装到全局二进制目录（默认 /usr/local/bin，Windows Git Bash 为 ~/bin，
#             BRAIN_GLOBAL_BIN 可改）
#   --link    安装到本技能目录 bin/ 下，并在全局目录建软链接指向它（单一二进制源：
#             此后重跑本脚本升级技能即全局生效）
# 升级时包内 SKILL.md（v0.10.3 起）与安装器自身（v0.10.5 起）随 Release 发布，
# 会同步/自更新技能目录副本，保证文档、安装器与二进制同版本出厂。
# 卸载（v0.10.6 起）：--uninstall 按安装形态反向拆除（全局软链/全局二进制/技能
# 目录 bin/），幂等、零网络；配置文件（内含 api_key）与技能目录文档不动——
# 凭据文件的删除应由用户亲手完成。

# 界面语言检测（与 Go 侧 internal/i18n 同构，须保持纯 POSIX 以便 sh/dash 守卫复用）：
# BRAIN_LANG > LC_ALL > LC_MESSAGES > LANG（zh* → 中文，其余非空 → 英文）> UTC+8 时区兜底。
detect_lang() {
  for v in "${BRAIN_LANG:-}" "${LC_ALL:-}" "${LC_MESSAGES:-}" "${LANG:-}"; do
    if [ -n "$v" ]; then
      case "$v" in
        [Zz][Hh]*) echo "zh" ;;
        *) echo "en" ;;
      esac
      return
    fi
  done
  if [ "$(date +%z 2>/dev/null || true)" = "+0800" ]; then
    echo "zh"
  else
    echo "en"
  fi
}

UI_LANG="$(detect_lang)"
say() {
  if [ "$UI_LANG" = "zh" ]; then
    printf '%s\n' "$1"
  else
    printf '%s\n' "$2"
  fi
}
say_err() {
  say "$1" "$2" >&2
}

# 守卫（本段须保持纯 POSIX，且位于 set -o pipefail 之前）：脚本使用 bash 方言，
# 被 sh/dash 显式调用时给一行明确指引，而不是在 set 行抛晦涩的 Illegal option。
if [ -z "${BASH_VERSION:-}" ]; then
  say_err "错误: 本脚本需要 bash（sh/dash 缺少所需特性），请改用: bash $0 ..." \
    "error: this installer requires bash (sh/dash is not supported); run: bash $0 ..."
  exit 1
fi
set -euo pipefail

REPO="icheer/brain-cli"
DIR="$(cd "$(dirname "$0")" && pwd)"

usage() {
  if [ "$UI_LANG" = "zh" ]; then
    cat <<'EOF'
用法: bash install.sh [--global | --link | --uninstall]
  (无参数)   首次装到本技能目录 bin/ 下；已有安装时自动检测既有形态按同模式升级
            （全局软链=--link；全局二进制=--global；两处皆有=都更新；仅技能目录=就地升级）
  --global  安装到全局二进制目录（默认 /usr/local/bin；不可写时自动 sudo）
  --link    安装到本技能目录 bin/ 下并在全局目录建软链接
            （单一二进制源：此后重跑 install.sh 升级技能即全局生效；
             Windows Git Bash 软链接不可靠，退化为复制）
  --uninstall 卸载：移除探测到的全部安装（全局软链/全局二进制/技能目录 bin/），
              幂等（未安装也 exit 0）、零网络；配置文件（内含 api_key）与
              SKILL.md/本脚本不动（彻底移除技能=删除整个技能目录）
升级说明: 重跑 install.sh 会同步包内 SKILL.md（v0.10.3 起）并自更新安装器自身
          （v0.10.5 起，原子替换，新逻辑下次运行生效）；也可直接运行 brain update
环境变量: BRAIN_INSTALL_DIR 覆盖技能目录安装位置；BRAIN_GLOBAL_BIN 覆盖全局目录
          （默认 Unix /usr/local/bin，Windows Git Bash ~/bin——Git Bash 在其
            存在时自动加入 PATH，无需管理员）；GITHUB_TOKEN/GH_TOKEN 提额 GitHub API
Windows:  在 Git Bash 或 WSL 下运行；Git Bash 产物为 brain.exe，WSL 走 Linux 路径
EOF
  else
    cat <<'EOF'
Usage: bash install.sh [--global | --link | --uninstall]
  (no flags)  First run installs into this skill dir's bin/; subsequent runs auto-detect
              the existing layout and upgrade in the same mode
              (global symlink=--link; global binary=--global; both=update both; skill-dir=in-place)
  --global    Install into the global bin dir (default /usr/local/bin; uses sudo if needed)
  --link      Install into this skill dir's bin/ and symlink into the global bin dir
              (single binary source; falls back to copy on Windows Git Bash)
  --uninstall Remove all detected brain binaries/symlinks (global and skill-dir bin/);
              idempotent and offline; config.yaml (contains api_key) and SKILL.md stay untouched
Upgrade:  Re-running install.sh (or `brain update`) syncs SKILL.md and atomically self-updates
          install.sh from the release archive.
Env vars: BRAIN_INSTALL_DIR overrides skill bin dir; BRAIN_GLOBAL_BIN overrides global bin dir
          (default /usr/local/bin on Unix, ~/bin on Windows Git Bash);
          GITHUB_TOKEN / GH_TOKEN authenticates GitHub API requests.
EOF
  fi
}

MODE="local"
MODE_FLAGS=""
UNINSTALL=0
while [ $# -gt 0 ]; do
  case "$1" in
    --global|--link)
      if [ -n "$MODE_FLAGS" ]; then
        say_err "错误: --global/--link/--uninstall 互斥" "error: --global, --link, and --uninstall are mutually exclusive"
        exit 1
      fi
      MODE_FLAGS="$1"
      MODE="${1#--}"
      ;;
    --uninstall)
      if [ -n "$MODE_FLAGS" ]; then
        say_err "错误: --global/--link/--uninstall 互斥" "error: --global, --link, and --uninstall are mutually exclusive"
        exit 1
      fi
      MODE_FLAGS="$1"
      UNINSTALL=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      say_err "未知参数: $1（--help 查看用法）" "unknown flag: $1 (see --help)"
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
    *)
      say_err "不支持的系统: $(uname -s)" "unsupported OS: $(uname -s)"
      return 1
      ;;
  esac
}

arch_name() {
  case "$(uname -m)" in
    x86_64 | amd64) echo amd64 ;;
    aarch64 | arm64) echo arm64 ;;
    *)
      say_err "不支持的架构: $(uname -m)" "unsupported architecture: $(uname -m)"
      return 1
      ;;
  esac
}

OS="$(os_name)"
ARCH="$(arch_name)"

# goreleaser 对 windows 产物恒追加 .exe（Release 包内成员即 brain.exe）
EXT=""
if [ "$OS" = "windows" ]; then
  EXT=".exe"
fi

# 全局目录默认值：Unix /usr/local/bin；Windows Git Bash 改用 $HOME/bin
if [ -n "${BRAIN_GLOBAL_BIN:-}" ]; then
  GLOBAL_BIN="${BRAIN_GLOBAL_BIN}"
elif [ "$OS" = "windows" ]; then
  GLOBAL_BIN="${HOME}/bin"
else
  GLOBAL_BIN="/usr/local/bin"
fi

# 卸载（--uninstall）：与安装侧同一套位置探测反向拆除，幂等、零网络。
do_uninstall() {
  GBIN="${GLOBAL_BIN}/brain${EXT}"
  LBIN="${BRAIN_INSTALL_DIR:-$DIR/bin}/brain${EXT}"
  REMOVED=0

  if [ -e "$GBIN" ] || [ -L "$GBIN" ]; then
    GDESC_ZH="二进制"
    GDESC_EN="binary"
    if [ -L "$GBIN" ]; then
      GDESC_ZH="软链"
      GDESC_EN="symlink"
    fi
    if rm -f "$GBIN" 2>/dev/null; then
      say "已移除全局${GDESC_ZH}: ${GBIN}" "removed global ${GDESC_EN}: ${GBIN}"
      REMOVED=1
    elif command -v sudo >/dev/null 2>&1; then
      sudo rm -f "$GBIN"
      say "已移除全局${GDESC_ZH}: ${GBIN}（sudo）" "removed global ${GDESC_EN}: ${GBIN} (sudo)"
      REMOVED=1
    else
      say_err "错误: ${GBIN} 不可删且系统无 sudo，请手动删除" \
        "error: cannot remove ${GBIN} (no permission and sudo unavailable); remove it manually"
    fi
  fi

  if [ -e "$LBIN" ]; then
    LDIR="$(dirname "$LBIN")"
    if rm -f "$LBIN" 2>/dev/null; then
      say "已移除: ${LBIN}" "removed: ${LBIN}"
      REMOVED=1
      rmdir "$LDIR" 2>/dev/null || true
    else
      say_err "提示: ${LBIN} 不可删（权限），请手动删除" \
        "note: cannot remove ${LBIN} (permission denied); remove it manually"
    fi
  fi

  if [ "$REMOVED" -eq 0 ]; then
    say "未发现已安装的 brain 二进制（${GBIN} / ${LBIN}），无可卸载项" \
      "no installed brain binary found (${GBIN} / ${LBIN}); nothing to uninstall"
  fi

  case "$OS" in
    darwin) CFG="$HOME/Library/Application Support/brain/config.yaml" ;;
    windows) CFG="${APPDATA:-<AppData>}/brain/config.yaml" ;;
    *) CFG="${XDG_CONFIG_HOME:-$HOME/.config}/brain/config.yaml" ;;
  esac
  say "配置文件未动（内含 api_key，如需彻底清理请自行删除）: ${CFG}" \
    "config file kept (contains api_key; delete manually if desired): ${CFG}"
  say "技能目录的 SKILL.md 与本脚本未动；彻底移除技能时删除整个技能目录即可" \
    "SKILL.md and install.sh kept; delete the skill directory to remove the skill completely"
}

if [ "$UNINSTALL" -eq 1 ]; then
  do_uninstall
  exit 0
fi

# fetch_latest_ver 查询最新 Release tag：支持 GITHUB_TOKEN/GH_TOKEN，
# 若 api.github.com 因共享 IP 触发 60 次/小时未认证限流（403/429），自动回退 302 重定向解析 tag。
fetch_latest_ver() {
  local tok="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
  local api_json=""
  if [ -n "$tok" ]; then
    api_json="$(curl -fsSL -H "Authorization: Bearer ${tok}" "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null || true)"
  else
    api_json="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null || true)"
  fi
  local ver=""
  if [ -n "$api_json" ]; then
    ver="$(printf '%s\n' "$api_json" | grep -o '"tag_name": *"[^"]*"' | head -1 | sed 's/.*"v/v/;s/"$//' || true)"
  fi
  if [ -n "$ver" ]; then
    printf '%s\n' "$ver"
    return 0
  fi
  local eff=""
  eff="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest" 2>/dev/null || true)"
  case "$eff" in
    */releases/tag/*)
      ver="${eff##*/releases/tag/}"
      ver="${ver%%\?*}"
      ver="${ver%%/}"
      if [ -n "$ver" ]; then
        printf '%s\n' "$ver"
        return 0
      fi
      ;;
  esac
  return 1
}

VER="$(fetch_latest_ver || true)"
if [ -z "$VER" ]; then
  say_err "获取最新版本失败（检查网络/仓库 ${REPO}）" "failed to resolve latest release version (check network or ${REPO})"
  exit 1
fi

BASE="brain_${VER#v}_${OS}_${ARCH}"
URL="https://github.com/${REPO}/releases/download/${VER}/${BASE}.tar.gz"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# 智能升级检测（仅无参数调用时；显式 --global/--link 精确执行不检测）：
# 注意：brain update 委托前会把正在运行的二进制自改名为 .old，故 .old 同样视作该位置存在既有安装。
if [ -z "$MODE_FLAGS" ]; then
  GBIN="${GLOBAL_BIN}/brain${EXT}"
  LBIN="${BRAIN_INSTALL_DIR:-$DIR/bin}/brain${EXT}"
  HAS_GBIN=0
  HAS_LBIN=0
  if [ -e "$GBIN" ] || [ -e "${GBIN}.old" ]; then HAS_GBIN=1; fi
  if [ -e "$LBIN" ] || [ -e "${LBIN}.old" ]; then HAS_LBIN=1; fi
  if [ -L "$GBIN" ]; then
    MODE="link"
    say "检测到全局软链（--link 形态），按同模式升级" "detected global symlink (--link layout); upgrading in same mode"
  elif [ "$HAS_GBIN" -eq 1 ] && [ "$HAS_LBIN" -eq 1 ]; then
    MODE="both"
    say "检测到全局与技能目录各有一份二进制（Git Bash 的 --link 复制形态或双重安装），两处均更新" \
      "detected binaries in both global and skill dirs (dual-copy layout); updating both"
  elif [ "$HAS_GBIN" -eq 1 ]; then
    MODE="global"
    say "检测到全局二进制（--global 形态），按同模式升级" "detected global binary (--global layout); upgrading in same mode"
  elif [ "$HAS_LBIN" -eq 1 ]; then
    say "检测到技能目录二进制，就地升级" "detected skill-dir binary; upgrading in place"
  fi
fi

say "下载 brain ${VER} (${OS}/${ARCH})" "downloading brain ${VER} (${OS}/${ARCH})"
curl -fsSL "$URL" -o "$TMP/${BASE}.tar.gz"
curl -fsSL "https://github.com/${REPO}/releases/download/${VER}/SHA256SUMS" -o "$TMP/SHA256SUMS"

# sha256_file 跨平台计算文件 SHA256：依次兼容 Linux/Git Bash (sha256sum)、
# macOS 原生 (shasum -a 256) 与通用兜底 (openssl)。
sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print tolower($1)}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print tolower($1)}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$1" | awk '{print tolower($NF)}'
  else
    say_err "错误: 未找到 sha256sum / shasum / openssl，无法完成 SHA256 校验" \
      "error: none of sha256sum / shasum / openssl found for SHA256 verification"
    return 1
  fi
}

# 校验压缩包哈希（Fail-Closed：缺失条目或摘要不符一律中止安装，拒绝执行未校验二进制）
WANT="$(awk -v f="${BASE}.tar.gz" '$2 == f || $2 == "*"f {print tolower($1); exit}' "$TMP/SHA256SUMS")"
if [ -z "$WANT" ]; then
  say_err "错误: SHA256SUMS 中未找到 ${BASE}.tar.gz 的校验记录，中止安装" \
    "error: ${BASE}.tar.gz not found in SHA256SUMS; aborting install"
  exit 1
fi
GOT="$(sha256_file "$TMP/${BASE}.tar.gz")"
if [ "$GOT" != "$WANT" ]; then
  say_err "错误: SHA256 校验失败（期望 ${WANT}，实际 ${GOT}）" \
    "error: SHA256 mismatch (expected ${WANT}, got ${GOT})"
  exit 1
fi
tar -xzf "$TMP/${BASE}.tar.gz" -C "$TMP" "brain${EXT}"

# v0.10.3 起包内含技能文档，v0.10.5 起包内另含安装器自身；对旧版本包/布局变化容错提取
SKILL_SRC=""
INST_SRC=""
for cand in "skills/brain/SKILL.md" "SKILL.md"; do
  if tar -xzf "$TMP/${BASE}.tar.gz" -C "$TMP" "$cand" 2>/dev/null; then
    if [ -f "$TMP/$cand" ]; then SKILL_SRC="$TMP/$cand"; break; fi
  fi
done
for cand in "skills/brain/install.sh" "install.sh"; do
  if tar -xzf "$TMP/${BASE}.tar.gz" -C "$TMP" "$cand" 2>/dev/null; then
    if [ -f "$TMP/$cand" ]; then INST_SRC="$TMP/$cand"; break; fi
  fi
done

ensure_global_dir() {
  if [ -d "$GLOBAL_BIN" ]; then return 0; fi
  if mkdir -p "$GLOBAL_BIN" 2>/dev/null; then return 0; fi
  if command -v sudo >/dev/null 2>&1; then
    sudo mkdir -p "$GLOBAL_BIN"
    return 0
  fi
  say_err "错误: 无法创建 ${GLOBAL_BIN}（无权限且系统无 sudo）；设 BRAIN_GLOBAL_BIN=<可写目录>（如 ~/.local/bin）后重试" \
    "error: cannot create ${GLOBAL_BIN} (permission denied and sudo unavailable); retry with BRAIN_GLOBAL_BIN=<writable-dir> (e.g. ~/.local/bin)"
  return 1
}

put_global() {
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
    say_err "错误: ${GLOBAL_BIN} 不可写且系统无 sudo；设 BRAIN_GLOBAL_BIN=<可写目录>（如 ~/.local/bin）后重试" \
      "error: ${GLOBAL_BIN} is not writable and sudo is unavailable; retry with BRAIN_GLOBAL_BIN=<writable-dir> (e.g. ~/.local/bin)"
    exit 1
  fi
}

link_global() {
  ensure_global_dir
  if [ -w "$GLOBAL_BIN" ]; then
    ln -sfn "$1" "$GLOBAL_BIN/brain${EXT}"
  elif command -v sudo >/dev/null 2>&1; then
    sudo ln -sfn "$1" "$GLOBAL_BIN/brain${EXT}"
  else
    say_err "错误: ${GLOBAL_BIN} 不可写且系统无 sudo；设 BRAIN_GLOBAL_BIN=<可写目录>（如 ~/.local/bin）后重试" \
      "error: ${GLOBAL_BIN} is not writable and sudo is unavailable; retry with BRAIN_GLOBAL_BIN=<writable-dir> (e.g. ~/.local/bin)"
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
    say_err "Windows Git Bash 软链接不可靠，--link 退化为复制到 ${GLOBAL_BIN}" \
      "symlinks are unreliable on Windows Git Bash; --link falls back to copying into ${GLOBAL_BIN}"
    put_global "$TARGET/brain${EXT}"
  else
    link_global "$TARGET/brain${EXT}"
  fi
fi

# 同步技能文档：包内 SKILL.md 与二进制同版本出厂
if [ -n "$SKILL_SRC" ] && [ -f "$DIR/SKILL.md" ]; then
  if ! cmp -s "$SKILL_SRC" "$DIR/SKILL.md"; then
    if cp "$SKILL_SRC" "$DIR/SKILL.md" 2>/dev/null; then
      say "SKILL.md 已同步至 ${VER}" "SKILL.md synced to ${VER}"
    else
      say_err "提示: 技能目录不可写，SKILL.md 未能同步" "note: skill dir not writable; SKILL.md was not synced"
    fi
  fi
fi

# 安装器原子自更新（同目录临时文件 + mv 换新 inode，不影响当前运行中的旧脚本 fd）
if [ -n "$INST_SRC" ] && [ -f "$DIR/SKILL.md" ] && [ -f "$DIR/install.sh" ] &&
   ! cmp -s "$INST_SRC" "$DIR/install.sh"; then
  NEWSH="$DIR/.install.sh.new"
  if cp "$INST_SRC" "$NEWSH" 2>/dev/null && chmod 0755 "$NEWSH" 2>/dev/null; then
    if mv -f "$NEWSH" "$DIR/install.sh" 2>/dev/null; then
      say "install.sh 已自更新至 ${VER}（新逻辑自下次运行生效）" \
        "install.sh self-updated to ${VER} (takes effect on next run)"
    else
      rm -f "$NEWSH"
      say_err "提示: install.sh 自更新失败（文件被占用或不可写）；从仓库重新复制 skills/brain/ 后即恢复自动" \
        "note: install.sh self-update failed (file busy or read-only); re-copy skills/brain/ from the repo to restore auto-updates"
    fi
  else
    rm -f "$NEWSH" 2>/dev/null || true
    say_err "提示: 技能目录不可写，install.sh 未能自更新；从仓库重新复制 skills/brain/ 后即恢复自动" \
      "note: skill dir not writable; install.sh was not self-updated"
  fi
fi

if [ "$MODE" = "global" ]; then
  "$GLOBAL_BIN/brain${EXT}" --version
else
  "$TARGET/brain${EXT}" --version
fi

if [ "$MODE" != "local" ]; then
  case ":$PATH:" in
    *":$GLOBAL_BIN:"*) ;;
    *) say_err "提示: ${GLOBAL_BIN} 不在当前 PATH 中（新开终端/自行加入后全局生效）" \
         "note: ${GLOBAL_BIN} is not in PATH yet (add it or open a new shell)" ;;
  esac
fi

case "$MODE" in
  local)
    say "完成。调用方式：${TARGET}/brain${EXT} ask -m fast \"...\"（或把 ${TARGET} 加入 PATH；终端全局安装可加 --link）" \
      "Done. Run: ${TARGET}/brain${EXT} ask -m fast \"...\" (or add ${TARGET} to PATH; pass --link for global symlink)"
    ;;
  global)
    say "完成。全局可用：brain ask -m fast \"...\"" "Done. Available globally: brain ask -m fast \"...\""
    ;;
  both)
    say "完成。技能目录: ${TARGET}/brain${EXT}；全局: ${GLOBAL_BIN}/brain${EXT}（两处均已更新；Windows Git Bash 下即 --link 的复制形态）" \
      "Done. Skill dir: ${TARGET}/brain${EXT}; global: ${GLOBAL_BIN}/brain${EXT} (both updated)"
    ;;
  link)
    say "完成。技能目录: ${TARGET}/brain${EXT}；全局: ${GLOBAL_BIN}/brain${EXT} → ${TARGET}/brain${EXT}（重跑 install.sh 升级即全局生效；Windows Git Bash 下为复制副本）" \
      "Done. Skill dir: ${TARGET}/brain${EXT}; global: ${GLOBAL_BIN}/brain${EXT} -> ${TARGET}/brain${EXT}"
    ;;
esac
