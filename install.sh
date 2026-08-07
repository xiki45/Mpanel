#!/usr/bin/env bash
#
# MPanel 一键安装脚本
# 适用于 Debian 12 / Ubuntu 22.04+ (amd64 / arm64)
#
# 用法:
#   sudo bash install.sh            # root 系统级安装
#   bash install.sh                  # 普通用户级安装 (systemd --user)
#   bash install.sh --uninstall       # 卸载
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
# 变量
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ACTION="install"
MPANEL_VERSION=""
INSTALL_MIHOMO="false"

# 解析参数
while [[ $# -gt 0 ]]; do
    case "$1" in
        --uninstall) ACTION="uninstall"; shift ;;
        --install-mihomo) INSTALL_MIHOMO="true"; shift ;;
        --help|-h)
            cat <<'EOF'
MPanel 一键安装脚本

用法:
  sudo bash install.sh [选项]     root 系统级安装
  bash install.sh [选项]          普通用户级安装

选项:
  --uninstall        卸载 MPanel
  --install-mihomo   同时下载安装 mihomo（如果尚未安装）
  --help             显示帮助
EOF
            exit 0 ;;
        *) die "未知参数: $1（使用 --help 查看帮助）" ;;
    esac
done

# ---------------------------------------------------------------------------
# 检测环境
# ---------------------------------------------------------------------------
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) die "不支持的架构: $ARCH（仅支持 amd64 和 arm64）" ;;
esac

OS_ID="$(. /etc/os-release 2>/dev/null && echo "$ID" || echo "")"
OS_LIKE="$(. /etc/os-release 2>/dev/null && echo "$ID_LIKE" || echo "")"
if [[ "$OS_ID" != "debian" && "$OS_ID" != "ubuntu" ]] && ! echo "$OS_LIKE" | grep -qw debian; then
    warn "当前系统不是 Debian/Ubuntu，脚本可能不完全兼容。继续安装..."
fi

# 检测是否以 root 运行
if [[ "$(id -u)" -eq 0 ]]; then
    INSTALL_MODE="system"
    MPANEL_USER="root"
    MPANEL_GROUP="root"
    BIN_DIR="/usr/local/bin"
    CONF_DIR="/etc/mpanel"
    CONF_FILE="$CONF_DIR/mpanel.env"
    SERVICE_DIR="/etc/systemd/system"
    SERVICE_FILE="$SERVICE_DIR/mpanel.service"
    SERVICE_NAME="mpanel.service"
    SYSTEMCTL="systemctl"
else
    INSTALL_MODE="user"
    MPANEL_USER="$(whoami)"
    MPANEL_GROUP="$(id -gn)"
    BIN_DIR="$HOME/.local/bin"
    CONF_DIR="$HOME/.config/mpanel"
    CONF_FILE="$CONF_DIR/mpanel.env"
    SERVICE_DIR="$HOME/.config/systemd/user"
    SERVICE_FILE="$SERVICE_DIR/mpanel.service"
    SERVICE_NAME="mpanel.service"
    SYSTEMCTL="systemctl --user"

    # 确保用户 lingering 启用，否则用户级 service 在退出登录后会停止
    info "当前为普通用户安装模式，将启用 lingering 以保证服务持续运行..."
    if ! loginctl enable-linger "$MPANEL_USER" 2>/dev/null; then
        warn "无法启用 lingering（可能需要 sudo 权限）。服务在退出登录后可能停止。"
        warn "请手动执行: sudo loginctl enable-linger $MPANEL_USER"
    fi
fi

# mihomo 配置默认路径
MIHOMO_CONFIG_PATH="/etc/mihomo/config.yaml"
MIHOMO_BINARY="/usr/local/bin/mihomo"
MIHOMO_SERVICE="mihomo.service"
MIHOMO_API_URL="http://127.0.0.1:9090"
MIHOMO_API_SECRET=""

# 普通用户安装时，mihomo 配置路径可能不同
if [[ "$INSTALL_MODE" == "user" ]]; then
    MIHOMO_CONFIG_PATH="$HOME/.config/mihomo/config.yaml"
    MIHOMO_BINARY="$HOME/.local/bin/mihomo"
    MIHOMO_SERVICE="mihomo.service"
fi

# ---------------------------------------------------------------------------
# 工具函数
# ---------------------------------------------------------------------------
rand_str() {
    if command -v openssl >/dev/null 2>&1; then
        openssl rand -base64 "$1" 2>/dev/null | tr -d '\n/+=' | head -c "$1"
    else
        # fallback: 使用 /dev/urandom
        head -c "$1" /dev/urandom | base64 | tr -d '\n/+=' | head -c "$1"
    fi
}

check_mihomo() {
    if [[ -x "$MIHOMO_BINARY" ]]; then
        ok "mihomo 已安装: $MIHOMO_BINARY"
        return 0
    fi

    if command -v mihomo >/dev/null 2>&1; then
        local p
        p="$(command -v mihomo)"
        ok "mihomo 已安装: $p"
        MIHOMO_BINARY="$p"
        return 0
    fi

    if [[ "$INSTALL_MIHOMO" == "true" ]]; then
        install_mihomo
        return $?
    fi

    warn "未检测到 mihomo。MPanel 依赖 mihomo 才能正常工作。"
    warn "请先安装 mihomo，或使用 --install-mihomo 选项自动安装。"
    warn "安装命令: sudo bash install.sh --install-mihomo"
    return 1
}

install_mihomo() {
    local version="v1.19.18"
    local url="https://github.com/MetaCubeX/mihomo/releases/download/${version}/mihomo-linux-${ARCH}-${version}.gz"
    local tmpfile="/tmp/mihomo-${version}.gz"

    info "正在下载 mihomo ${version} (${ARCH})..."

    if ! curl -fsSL -o "$tmpfile" "$url"; then
        error "mihomo 下载失败"
        return 1
    fi

    gunzip -f "$tmpfile"
    local bin="${tmpfile%.gz}"

    if [[ "$INSTALL_MODE" == "system" ]]; then
        install -d -m 0755 "$(dirname "$MIHOMO_BINARY")"
        install -m 0755 "$bin" "$MIHOMO_BINARY"
    else
        install -d -m 0755 "$(dirname "$MIHOMO_BINARY")"
        install -m 0755 "$bin" "$MIHOMO_BINARY"
    fi

    rm -f "$bin"
    ok "mihomo 安装完成: $MIHOMO_BINARY ($version)"

    # 创建 mihomo 配置目录和最小配置（如果不存在）
    local conf_dir
    conf_dir="$(dirname "$MIHOMO_CONFIG_PATH")"
    if [[ ! -f "$MIHOMO_CONFIG_PATH" ]]; then
        info "创建最小 mihomo 配置..."
        install -d -m 0755 "$conf_dir"
        cat > "$MIHOMO_CONFIG_PATH" <<'EOF'
mixed-port: 7890
allow-lan: false
mode: rule
log-level: info
external-controller: 127.0.0.1:9090
secret: ""

proxies: []
proxy-groups: []
rules:
  - MATCH,DIRECT
EOF
        if [[ "$INSTALL_MODE" == "system" ]]; then
            chmod 0644 "$MIHOMO_CONFIG_PATH"
        fi
        ok "mihomo 配置已创建: $MIHOMO_CONFIG_PATH"
        warn "请编辑配置文件添加代理节点和设置 secret。"
    fi

    # 创建 mihomo systemd service（仅系统模式且不存在时）
    if [[ "$INSTALL_MODE" == "system" ]] && ! systemctl list-unit-files 2>/dev/null | grep -q "mihomo.service"; then
        info "创建 mihomo systemd service..."
        cat > /etc/systemd/system/mihomo.service <<'EOF'
[Unit]
Description=mihomo Daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=BIN_PLACEHOLDER -d CONFDIR_PLACEHOLDER
Restart=on-failure
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF
        sed -i "s|BIN_PLACEHOLDER|$MIHOMO_BINARY|; s|CONFDIR_PLACEHOLDER|$conf_dir|" /etc/systemd/system/mihomo.service
        systemctl daemon-reload
        systemctl enable --now mihomo.service
        ok "mihomo service 已创建并启动"
    fi
}

# ---------------------------------------------------------------------------
# 卸载
# ---------------------------------------------------------------------------
do_uninstall() {
    info "开始卸载 MPanel..."

    # 停止服务
    if [[ "$INSTALL_MODE" == "system" ]]; then
        if systemctl list-unit-files 2>/dev/null | grep -q "mpanel.service"; then
            systemctl disable --now mpanel.service 2>/dev/null || true
            ok "已停止并禁用 mpanel.service"
        fi
    else
        if systemctl --user list-unit-files 2>/dev/null | grep -q "mpanel.service"; then
            systemctl --user disable --now mpanel.service 2>/dev/null || true
            ok "已停止并禁用用户级 mpanel.service"
        fi
    fi

    # 删除二进制
    if [[ -f "$BIN_DIR/mpanel" ]]; then
        rm -f "$BIN_DIR/mpanel"
        ok "已删除二进制: $BIN_DIR/mpanel"
    fi

    # 删除 service 文件
    if [[ -f "$SERVICE_FILE" ]]; then
        rm -f "$SERVICE_FILE"
        ok "已删除 service 文件: $SERVICE_FILE"
    fi

    # 删除配置（提示但不强制）
    if [[ -f "$CONF_FILE" ]]; then
        read -rp "是否删除配置文件 $CONF_FILE？[y/N] " confirm
        if [[ "$confirm" =~ ^[Yy]$ ]]; then
            rm -f "$CONF_FILE"
            rmdir "$CONF_DIR" 2>/dev/null || true
            ok "已删除配置目录: $CONF_DIR"
        else
            info "保留配置文件: $CONF_FILE"
        fi
    fi

    # 重载 systemd
    $SYSTEMCTL daemon-reload 2>/dev/null || true

    ok "MPanel 卸载完成"
    exit 0
}

# ---------------------------------------------------------------------------
# 安装
# ---------------------------------------------------------------------------
do_install() {
    info "MPanel 一键安装"
    info "  安装模式: ${BOLD}$INSTALL_MODE${NC}"
    info "  用户: $MPANEL_USER"
    info "  架构: $ARCH"
    info "  二进制目录: $BIN_DIR"
    info "  配置目录: $CONF_DIR"
    echo ""

    # 1. 检查并安装依赖
    info "[1/6] 检查依赖..."

    if ! command -v curl >/dev/null 2>&1; then
        info "安装 curl..."
        if [[ "$INSTALL_MODE" == "system" ]]; then
            apt-get update -qq && apt-get install -y -qq curl >/dev/null 2>&1 || die "无法安装 curl"
        else
            die "缺少 curl，请先安装: sudo apt-get install curl"
        fi
    fi

    check_mihomo || true

    echo ""

    # 2. 获取 MPanel 二进制（优先从 Release 下载，避免弱机编译）
    info "[2/6] 安装 MPanel 二进制..."

    local download_url=""
    local api_resp

    # 尝试从 GitHub Releases 获取预编译二进制
    if api_resp="$(curl -fsSL "https://api.github.com/repos/xiki45/Mpanel/releases/latest" 2>/dev/null)"; then
        download_url="$(echo "$api_resp" | grep -o "https://[^\"]*mpanel-linux-${ARCH}[^\"]*" | head -1)"
    fi

    if [[ -n "$download_url" ]]; then
        info "从 GitHub Release 下载预编译二进制 (${ARCH})..."
        local tmpfile="/tmp/mpanel-download-$$"
        if ! curl -fsSL -o "$tmpfile" "$download_url"; then
            die "下载失败: $download_url"
        fi
        chmod +x "$tmpfile"
        install -d -m 0755 "$BIN_DIR"
        install -m 0755 "$tmpfile" "$BIN_DIR/mpanel"
        rm -f "$tmpfile"
        ok "二进制下载安装完成"
    else
        # Release 不存在时，尝试从源码编译
        local src_dir="$SCRIPT_DIR"
        local has_source="false"
        if [[ -f "$src_dir/go.mod" ]] && [[ -f "$src_dir/cmd/mpanel/main.go" ]]; then
            has_source="true"
        fi

        if [[ "$has_source" == "true" ]] && command -v go >/dev/null 2>&1; then
            info "未找到 Release，检测到源码和 Go，从源码编译..."
            local tmpbin="/tmp/mpanel-build-$$"
            if (cd "$src_dir" && CGO_ENABLED=0 go build -trimpath -o "$tmpbin" ./cmd/mpanel); then
                install -d -m 0755 "$BIN_DIR"
                install -m 0755 "$tmpbin" "$BIN_DIR/mpanel"
                rm -f "$tmpbin"
                ok "从源码编译安装完成"
            else
                die "源码编译失败，请检查 Go 环境"
            fi
        else
            warn "未找到预编译二进制 Release，且无源码/Go 环境可供编译。"
            info "请从以下方式选择："
            info "  1. 在 GitHub 创建 Release 并上传 mpanel-linux-${ARCH} 二进制"
            info "  2. 手动编译后放到 $BIN_DIR/mpanel"
            die "无法获取 MPanel 二进制"
        fi
    fi

    echo ""

    # 3. 生成配置
    info "[3/6] 生成配置文件..."

    install -d -m 0700 "$CONF_DIR"

    # 如果配置已存在，询问是否覆盖
    if [[ -f "$CONF_FILE" ]]; then
        ok "配置文件已存在: $CONF_FILE"
        read -rp "是否重新生成配置？（会覆盖现有配置）[y/N] " confirm
        if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
            info "保留现有配置"
            echo ""
            # 跳到 service 创建
            goto_service=true
        fi
    fi

    if [[ "${goto_service:-false}" != "true" ]]; then
        # 交互式收集配置
        echo ""
        printf "${BOLD}--- MPanel 配置 ---${NC}\n"

        # 监听地址
        local listen_addr="127.0.0.1:8080"
        read -rp "监听地址 [${listen_addr}]: " input
        [[ -n "$input" ]] && listen_addr="$input"

        # 用户名
        local username="admin"
        read -rp "登录用户名 [${username}]: " input
        [[ -n "$input" ]] && username="$input"

        # 密码
        local password
        local gen_pass
        gen_pass="$(rand_str 16)"
        echo "  建议密码: $gen_pass"
        read -rp "登录密码（留空使用建议密码）: " input
        if [[ -z "$input" ]]; then
            password="$gen_pass"
        else
            password="$input"
        fi

        # Session secret
        local session_secret
        session_secret="$(rand_str 48)"

        # mihomo API
        local mihomo_url="$MIHOMO_API_URL"
        read -rp "mihomo API 地址 [${mihomo_url}]: " input
        [[ -n "$input" ]] && mihomo_url="$input"

        local mihomo_secret="$MIHOMO_API_SECRET"
        read -rp "mihomo API Secret（如有）: " input
        [[ -n "$input" ]] && mihomo_secret="$input"

        # mihomo 配置路径
        local mihomo_conf="$MIHOMO_CONFIG_PATH"
        read -rp "mihomo 配置路径 [${mihomo_conf}]: " input
        [[ -n "$input" ]] && mihomo_conf="$input"

        # mihomo 二进制路径
        local mihomo_bin="$MIHOMO_BINARY"
        read -rp "mihomo 二进制路径 [${mihomo_bin}]: " input
        [[ -n "$input" ]] && mihomo_bin="$input"

        echo ""

        # 写入配置文件
        cat > "$CONF_FILE" <<EOF
# MPanel 配置文件 - 由 install.sh 生成于 $(date '+%Y-%m-%d %H:%M:%S')
# 权限 0600，仅 $MPANEL_USER 可读

MPANEL_LISTEN_ADDR=${listen_addr}
MPANEL_USERNAME=${username}
MPANEL_PASSWORD=${password}
MPANEL_SESSION_SECRET=${session_secret}
MIHOMO_API_URL=${mihomo_url}
MIHOMO_API_SECRET=${mihomo_secret}
MIHOMO_CONFIG_PATH=${mihomo_conf}
MIHOMO_BINARY=${mihomo_bin}
MIHOMO_SERVICE=${MIHOMO_SERVICE}
EOF
        chmod 0600 "$CONF_FILE"
        chown "$MPANEL_USER:$MPANEL_GROUP" "$CONF_FILE"
        ok "配置文件已生成: $CONF_FILE"

        # 显示凭据摘要
        echo ""
        printf "${GREEN}${BOLD}=== 凭据摘要 ===${NC}\n"
        printf "  地址:   ${BOLD}http://$(hostname -I 2>/dev/null | awk '{print $1}' || echo 'localhost'):${listen_addr##*:}${NC}\n"
        printf "  用户名: ${BOLD}${username}${NC}\n"
        printf "  密码:   ${BOLD}${password}${NC}\n"
        printf "${YELLOW}  请妥善保存以上凭据！${NC}\n"
        echo ""
    fi

    # 4. 创建 systemd service
    info "[4/6] 配置 systemd..."

    install -d -m 0755 "$SERVICE_DIR"

    if [[ "$INSTALL_MODE" == "system" ]]; then
        # 系统级 service
        local conf_dir_for_rwp
        conf_dir_for_rwp="$(dirname "$(grep -oP '(?<=MIHOMO_CONFIG_PATH=).*' "$CONF_FILE" 2>/dev/null || echo "$MIHOMO_CONFIG_PATH")")"

        cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=MPanel mihomo management panel
After=network-online.target ${MIHOMO_SERVICE}
Wants=network-online.target

[Service]
Type=simple
User=${MPANEL_USER}
Group=${MPANEL_GROUP}
EnvironmentFile=${CONF_FILE}
ExecStart=${BIN_DIR}/mpanel
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=${conf_dir_for_rwp}

[Install]
WantedBy=multi-user.target
EOF
    else
        # 用户级 service
        cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=MPanel mihomo management panel
After=network-online.target

[Service]
Type=simple
EnvironmentFile=${CONF_FILE}
ExecStart=${BIN_DIR}/mpanel
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
EOF
    fi
    chmod 0644 "$SERVICE_FILE"
    ok "service 文件已创建: $SERVICE_FILE"

    echo ""

    # 5. 启动服务
    info "[5/6] 启动服务..."

    $SYSTEMCTL daemon-reload
    $SYSTEMCTL enable --now "$SERVICE_NAME" 2>/dev/null || {
        warn "自动启动失败，尝试手动启动..."
        $SYSTEMCTL start "$SERVICE_NAME" 2>/dev/null || true
    }

    sleep 2

    # 检查服务状态
    if $SYSTEMCTL is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
        ok "mpanel.service 已启动并运行"
    else
        warn "mpanel.service 未正常运行，请检查日志:"
        warn "  $SYSTEMCTL status mpanel.service"
        warn "  journalctl -u mpanel -n 30 --no-pager"
    fi

    echo ""

    # 6. 完成提示
    info "[6/6] 安装完成！"
    echo ""

    local listen_addr
    listen_addr="$(grep -oP '(?<=MPANEL_LISTEN_ADDR=).*' "$CONF_FILE" 2>/dev/null || echo "127.0.0.1:8080")"
    local host_ip
    host_ip="$(hostname -I 2>/dev/null | awk '{print $1}' || echo 'localhost')"

    printf "${GREEN}${BOLD}========================================${NC}\n"
    printf "${GREEN}${BOLD}  MPanel 安装完成！${NC}\n"
    printf "${GREEN}${BOLD}========================================${NC}\n"
    echo ""
    printf "  访问地址:  ${BOLD}http://${host_ip}:${listen_addr##*:}${NC}\n"
    printf "  本机访问:  ${BOLD}http://127.0.0.1:${listen_addr##*:}${NC}\n"
    echo ""
    printf "  常用命令:\n"
    printf "    查看状态:  ${BOLD}$SYSTEMCTL status mpanel${NC}\n"
    printf "    重启服务:  ${BOLD}$SYSTEMCTL restart mpanel${NC}\n"
    printf "    查看日志:  ${BOLD}journalctl -u mpanel -f${NC}\n"
    printf "    编辑配置:  ${BOLD}${EDITOR:-nano} ${CONF_FILE}${NC}\n"
    echo ""
    if [[ "$INSTALL_MODE" == "system" ]]; then
        printf "  TLS 反向代理（推荐）:\n"
        printf "    安装 Caddy 后编辑 ${BOLD}/etc/caddy/Caddyfile${NC} 指向 127.0.0.1:${listen_addr##*:}\n"
        echo ""
    fi
    printf "${YELLOW}  安全提示: 请勿将面板端口直接暴露到公网，建议配合反向代理使用 TLS。${NC}\n"
    echo ""
}

# ---------------------------------------------------------------------------
# 主入口
# ---------------------------------------------------------------------------
if [[ "$ACTION" == "uninstall" ]]; then
    do_uninstall
else
    do_install
fi
