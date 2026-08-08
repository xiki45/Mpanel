#!/usr/bin/env bash
#
# MPanel 一键更新脚本
# 从 GitHub Release 拉取最新预编译二进制并更新本地面板。
# 自动区分 root 系统级与普通用户级安装。
#
# 用法:
#   sudo bash update.sh                  # root 系统级更新（latest）
#   bash update.sh                       # 普通用户级更新（latest）
#   bash update.sh --version v1.1.0      # 更新到指定版本 tag
#   bash update.sh --check               # 仅检查是否有可用的更新，不执行
#   bash update.sh --help
#
set -euo pipefail

# ---------------------------------------------------------------------------
# 颜色与输出
# ---------------------------------------------------------------------------
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m'

info()  { printf "${BLUE}[INFO]${NC} %s\n" "$*"; }
ok()    { printf "${GREEN}[OK]${NC} %s\n" "$*"; }
warn()  { printf "${YELLOW}[WARN]${NC} %s\n" "$*"; }
error() { printf "${RED}[ERROR]${NC} %s\n" "$*" >&2; }
die()   { error "$*"; exit 1; }

# ---------------------------------------------------------------------------
# 变量与参数
# ---------------------------------------------------------------------------
REPO="xiki45/Mpanel"
VERSION=""
CHECK_ONLY="false"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --version)
            [[ $# -ge 2 ]] || die "参数 --version 需要一个版本号，例如 v1.1.0"
            VERSION="$2"; shift 2 ;;
        --check)
            CHECK_ONLY="true"; shift ;;
        --help|-h)
            cat <<'EOF'
MPanel 一键更新脚本

用法:
  sudo bash update.sh                  root 系统级更新（latest）
  bash update.sh                       普通用户级更新（latest）
  bash update.sh --version v1.1.0      更新到指定版本 tag
  bash update.sh --check               仅检查可更新版本，不执行

选项:
  --version <tag>   更新到指定版本（如 v1.1.0）
  --check           只检查最新版本，不更新
  --help            显示帮助
EOF
            exit 0 ;;
        *) die "未知参数: $1（使用 --help 查看帮助）" ;;
    esac
done

# ---------------------------------------------------------------------------
# 检测架构
# ---------------------------------------------------------------------------
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) die "不支持的架构: $ARCH（仅支持 amd64 和 arm64）" ;;
esac

# ---------------------------------------------------------------------------
# 检测安装模式（root 系统级 / 普通用户级），与 install.sh 保持一致
# ---------------------------------------------------------------------------
if [[ "$(id -u)" -eq 0 ]]; then
    INSTALL_MODE="system"
    BIN_DIR="/usr/local/bin"
    SERVICE_DIR="/etc/systemd/system"
    SYSTEMCTL="systemctl"
else
    INSTALL_MODE="user"
    BIN_DIR="$HOME/.local/bin"
    SERVICE_DIR="$HOME/.config/systemd/user"
    SYSTEMCTL="systemctl --user"
fi

BIN_PATH="$BIN_DIR/mpanel"
SERVICE_NAME="mpanel.service"

mode_label="用户级"; [[ "$INSTALL_MODE" == "system" ]] && mode_label="系统级"
info "更新模式: ${INSTALL_MODE}（${mode_label}）"
info "架构: ${ARCH}"

# ---------------------------------------------------------------------------
# 解析要下载的版本与 URL
# ---------------------------------------------------------------------------
download_url=""
release_tag=""

if [[ -n "$VERSION" ]]; then
    release_tag="$VERSION"
    # 指定的 Release（tag）资产
    api_url="https://api.github.com/repos/${REPO}/releases/tags/${release_tag}"
else
    api_url="https://api.github.com/repos/${REPO}/releases/latest"
fi

info "正在查询版本信息..."
api_resp="$(curl -fsSL "$api_url" 2>/dev/null || true)"
if [[ -z "$api_resp" ]]; then
    if [[ -n "$VERSION" ]]; then
        die "未找到 Release: ${release_tag}（${REPO}）"
    fi
    die "无法访问 GitHub Releases 接口（${api_url}）"
fi

# 匹配该架构的二进制资产下载地址（与 install.sh 一致的正则）
download_url="$(echo "$api_resp" | grep -o "https://[^\"]*mpanel-linux-${ARCH}[^\"]*" | head -1)"

if [[ -z "$download_url" ]]; then
    die "该 Release 中没有找到 mpanel-linux-${ARCH} 资产，无法更新。"
fi

# 从下载地址提取 release tag（形如 .../releases/download/<tag>/mpanel-linux-<arch>）
if [[ -z "$release_tag" ]]; then
    release_tag="$(echo "$download_url" | sed -n 's#.*/releases/download/\([^/]*\)/.*#\1#p')"
fi
[[ -z "$release_tag" ]] && release_tag="unknown"

# ---------------------------------------------------------------------------
# --check 模式：仅打印当前安装信息与可下载的最新版本
# ---------------------------------------------------------------------------
installed="false"
installed_time=""
if [[ -f "$BIN_PATH" ]]; then
    installed="true"
    installed_time="$(stat -c '%y' "$BIN_PATH" 2>/dev/null | cut -d'.' -f1 || echo '')"
fi

if [[ "$CHECK_ONLY" == "true" ]]; then
    if [[ "$installed" == "true" ]]; then
        info "当前已安装: ${BIN_PATH}"
        info "二进制更新于: ${installed_time:-未知}"
    else
        info "当前未安装 MPanel（未找到 ${BIN_PATH}）"
    fi
    info "可下载版本: ${release_tag}"
    info "下载地址:   ${download_url}"
    info "执行更新:   bash update.sh"
    exit 0
fi

# ---------------------------------------------------------------------------
# 下载并校验
# ---------------------------------------------------------------------------
info "下载 MPanel ${release_tag} (${ARCH})..."
tmpfile="$(mktemp /tmp/mpanel-update-XXXXXX)"

if ! curl -fsSL -o "$tmpfile" "$download_url"; then
    rm -f "$tmpfile"
    die "下载失败: $download_url"
fi

# 基础校验：非空 + ELF 可执行文件魔数
if [[ ! -s "$tmpfile" ]]; then
    rm -f "$tmpfile"
    die "下载的二进制为空，已中止更新。"
fi
magic="$(head -c 4 "$tmpfile" 2>/dev/null)"
if [[ "$magic" != $'\x7fELF' ]]; then
    rm -f "$tmpfile"
    die "下载的文件不是有效的 ELF 可执行文件，已中止更新。"
fi
chmod +x "$tmpfile"
ok "下载完成，校验通过 (${release_tag})"

# ---------------------------------------------------------------------------
# 备份并替换旧二进制
# ---------------------------------------------------------------------------
if [[ -f "$BIN_PATH" ]]; then
    backup="$BIN_PATH.bak.$(date +%Y%m%d%H%M%S)"
    cp -a "$BIN_PATH" "$backup"
    ok "已备份旧二进制到: $backup"
fi

install -d -m 0755 "$BIN_DIR"
if ! install -m 0755 "$tmpfile" "$BIN_PATH"; then
    rm -f "$tmpfile"
    die "写入 $BIN_PATH 失败（请检查权限）"
fi
rm -f "$tmpfile"
ok "已更新二进制: $BIN_PATH"

# ---------------------------------------------------------------------------
# 重启服务
# ---------------------------------------------------------------------------
if [[ "$INSTALL_MODE" == "system" ]]; then
    if systemctl list-unit-files "$SERVICE_NAME" >/dev/null 2>&1; then
        info "重启服务: systemctl restart ${SERVICE_NAME}"
        systemctl daemon-reload 2>/dev/null || true
        systemctl restart "$SERVICE_NAME" && ok "服务已重启" || warn "服务重启失败，请手动执行: systemctl restart mpanel"
    else
        warn "未检测到 ${SERVICE_NAME}，跳过服务重启。请手动启动: $BIN_PATH"
    fi
else
    if systemctl --user list-unit-files "$SERVICE_NAME" >/dev/null 2>&1; then
        info "重启服务: systemctl --user restart ${SERVICE_NAME}"
        systemctl --user daemon-reload 2>/dev/null || true
        systemctl --user restart "$SERVICE_NAME" && ok "服务已重启" || warn "服务重启失败，请手动执行: systemctl --user restart mpanel"
    else
        warn "未检测到用户级 ${SERVICE_NAME}，跳过服务重启。请手动启动: $BIN_PATH"
    fi
fi

# ---------------------------------------------------------------------------
# 完成
# ---------------------------------------------------------------------------
ok "MPanel 更新完成 (${release_tag})"
info "当前二进制: $BIN_PATH"
echo ""
info "后续管理:"
printf "    查看状态:  ${BOLD}${SYSTEMCTL} status mpanel${NC}\n"
printf "    重启服务:  ${BOLD}${SYSTEMCTL} restart mpanel${NC}\n"
