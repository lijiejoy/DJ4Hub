#!/bin/zsh
#
# DJ 4G Hub 启动脚本（双击运行）
#
# 流程：准备可执行文件 → 激活 Baiwang 4G 网卡 → 由 launchd 启动服务 → 打开管理页面
#
# 可选参数：
#   --demo          无硬件演示模式（跳过网卡激活）
#   --no-network    跳过 4G 网卡激活
#   --no-open       启动后不自动打开浏览器
#   -h, --help      显示帮助
#
set -u

APP_NAME="DJ 4G Hub"
LABEL="com.dj4hub.local"
ROOT_DIR="${0:A:h}"

# 源文件（iCloud 桌面目录，可能被系统移出本机）
# 可用 DJ4GHUB_SOURCE_BIN 指向其他位置的构建产物。
SRC_BIN="${DJ4GHUB_SOURCE_BIN:-${ROOT_DIR}/dist/dj4ghub-macos}"

# 运行时文件放在本地目录：launchd 无法执行仍留在 iCloud 里的可执行文件/配置，
# 否则会报 "Bootstrap failed: 5: Input/output error"。
SUPPORT_DIR="${HOME}/Library/Application Support/${APP_NAME}"
BIN_DIR="${SUPPORT_DIR}/bin"
BIN="${BIN_DIR}/dj4ghub-macos"
LOG_DIR="${HOME}/Library/Logs/${APP_NAME}"
SERVICE_LOG="${LOG_DIR}/dj4ghub-launchd.log"
ACTIVATE_LOG="${LOG_DIR}/dj4hub-activate.log"
PLIST="${SUPPORT_DIR}/${LABEL}.plist"
PID_FILE="${SUPPORT_DIR}/dj4ghub.pid"

URL="http://127.0.0.1:7575"
HEALTH_URL="${URL}/api/health"
DOMAIN="gui/$(id -u)"

DEMO=0
SKIP_NETWORK=0
OPEN_PAGE=1
BOOTSTRAP_ERROR=""
# 等待 iCloud 把文件下载回本机的最长秒数（可用环境变量覆盖，便于调试）
ICLOUD_WAIT_SECONDS="${DJ4GHUB_ICLOUD_TIMEOUT:-120}"
[[ "${ICLOUD_WAIT_SECONDS}" == <-> ]] || ICLOUD_WAIT_SECONDS=120

[[ "${DJ4GHUB_NO_OPEN:-0}" == "1" ]] && OPEN_PAGE=0

usage() {
  cat <<'EOF'
DJ 4G Hub 启动脚本

用法：
  ./start-dj4hub.command                 启动并打开管理页面
  ./start-dj4hub.command --demo          无硬件演示模式
  ./start-dj4hub.command --no-network    跳过 4G 网卡激活
  ./start-dj4hub.command --no-open       不自动打开浏览器

日志：
  ~/Library/Logs/DJ 4G Hub/dj4ghub-launchd.log
EOF
}

pause_and_exit() {
  print -u2 -- ""
  print -u2 -- "$1"
  if [[ -t 0 ]]; then
    print -n "按回车关闭此窗口..."
    read -r _
  fi
  exit 1
}

note() {
  print -r -- "$*"
}

health_ok() {
  curl -fsS --max-time 2 "${HEALTH_URL}" >/dev/null 2>&1
}

job_loaded() {
  launchctl print "${DOMAIN}/${LABEL}" >/dev/null 2>&1
}

service_pid() {
  launchctl print "${DOMAIN}/${LABEL}" 2>/dev/null | awk '/pid = / { print $3; exit }'
}

pid_alive() {
  [[ -n "${1:-}" ]] && kill -0 "$1" 2>/dev/null
}

port_busy() {
  /usr/sbin/lsof -nP -iTCP:7575 -sTCP:LISTEN >/dev/null 2>&1
}

pids_matching() {
  pgrep -f -- "$1" 2>/dev/null || true
}

kill_matching() {
  local pattern="$1" signal="${2:-TERM}" pid found=1
  while read -r pid; do
    [[ -z "${pid}" || "${pid}" == "$$" ]] && continue
    if kill -"${signal}" "${pid}" 2>/dev/null; then
      found=0
    fi
  done < <(pids_matching "${pattern}")
  return "${found}"
}

is_dataless() {
  [[ "$(stat -f '%Sf' "$1" 2>/dev/null)" == *dataless* ]]
}

# iCloud 会把桌面上的文件移出本机（dataless）。launchd 无法执行这种文件，
# 普通读取也可能长时间阻塞，所以先显式请求下载并等待。
materialize_source() {
  is_dataless "${SRC_BIN}" || return 0

  note "可执行文件当前不在本机（已被 iCloud 移出），正在请求下载..."
  /usr/bin/brctl download "${SRC_BIN}" >/dev/null 2>&1 || true
  local i=0
  while (( i < ICLOUD_WAIT_SECONDS )); do
    is_dataless "${SRC_BIN}" || return 0
    (( i > 0 && i % 10 == 0 )) && note "  仍在等待 iCloud 下载（${i}s）..."
    sleep 1
    (( i++ ))
  done
  return 1
}

copy_with_timeout() {
  local src="$1" dst="$2" timeout="${3:-${ICLOUD_WAIT_SECONDS}}" pid i=0
  cp -f "${src}" "${dst}" 2>/dev/null &
  pid=$!
  while (( i < timeout )); do
    if ! kill -0 "${pid}" 2>/dev/null; then
      wait "${pid}"
      return $?
    fi
    sleep 1
    (( i++ ))
  done
  kill "${pid}" 2>/dev/null || true
  wait "${pid}" 2>/dev/null || true
  return 1
}

install_binary() {
  if [[ ! -x "${SRC_BIN}" ]]; then
    pause_and_exit "找不到可执行文件：${SRC_BIN}"
  fi

  # 已安装副本复制后会重新签名，体积与源文件不完全一致，
  # 因此只用可执行性和源文件时间戳判断是否需要更新。
  local need=0
  if [[ ! -x "${BIN}" ]] || [[ ! -s "${BIN}" ]]; then
    need=1
  elif [[ "${SRC_BIN}" -nt "${BIN}" ]]; then
    need=1
  fi
  (( need )) || return 0

  # 清理上次异常中断留下的临时文件
  local stale
  for stale in "${BIN}".*.tmp(N); do
    rm -f "${stale}"
  done

  note "正在准备可执行文件..."
  local tmp="${BIN}.$$.tmp"
  local copied=0
  if materialize_source; then
    copy_with_timeout "${SRC_BIN}" "${tmp}" && copied=1
  else
    note "无法确认 iCloud 下载是否完成，尝试直接复制..."
    copy_with_timeout "${SRC_BIN}" "${tmp}" 15 && copied=1
  fi
  if (( ! copied )); then
    rm -f "${tmp}"
    pause_and_exit "无法从 iCloud 取回可执行文件（${ICLOUD_WAIT_SECONDS}s 内未完成下载）。请在“终端”中执行：
  brctl download ${SRC_BIN}
或把整个 dj4hub 文件夹移到本地目录（例如 ~/Developer）后重试。"
  fi
  chmod 755 "${tmp}"
  xattr -d com.apple.quarantine "${tmp}" >/dev/null 2>&1 || true
  codesign --force --sign - "${tmp}" >/dev/null 2>&1 || true
  mv -f "${tmp}" "${BIN}" || pause_and_exit "无法写入：${BIN}"
}

rotate_log() {
  [[ -f "${SERVICE_LOG}" ]] || return 0
  local size
  size="$(stat -f%z "${SERVICE_LOG}" 2>/dev/null || print 0)"
  if (( ${size:-0} > 5242880 )); then
    mv -f "${SERVICE_LOG}" "${SERVICE_LOG}.1"
  fi
}

write_plist() {
  local demo_arg=""
  (( DEMO )) && demo_arg=$'\n    <string>-demo</string>'

  cat >| "${PLIST}" <<PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${LABEL}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${BIN}</string>${demo_arg}
  </array>
  <key>WorkingDirectory</key>
  <string>${SUPPORT_DIR}</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <false/>
  <key>ThrottleInterval</key>
  <integer>10</integer>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
  <key>StandardOutPath</key>
  <string>${SERVICE_LOG}</string>
  <key>StandardErrorPath</key>
  <string>${SERVICE_LOG}</string>
</dict>
</plist>
PLIST_EOF

  if ! plutil -lint "${PLIST}" >/dev/null 2>&1; then
    pause_and_exit "生成的 launchd 配置无效：${PLIST}"
  fi
}

has_ecm_ip() {
  ifconfig -a 2>/dev/null | grep -q 'inet 192\.168\.225\.'
}

activate_network() {
  if (( DEMO )); then
    note "演示模式：跳过 4G 网卡激活。"
    return 0
  fi
  if (( SKIP_NETWORK )); then
    note "已跳过 4G 网卡激活。"
    return 0
  fi

  note "正在检查 4G 网卡..."
  local state
  if state="$(networksetup -getnetworkserviceenabled "Baiwang" 2>/dev/null)"; then
    if [[ "${state}" != "Enabled" ]]; then
      if networksetup -setnetworkserviceenabled "Baiwang" on >/dev/null 2>&1; then
        note "已启用 Baiwang 网络服务。"
      else
        note "警告：启用 Baiwang 网络服务失败。"
      fi
    fi
  fi

  if has_ecm_ip; then
    note "4G 网卡已经获得 IP，跳过模块激活。"
    return 0
  fi

  note "正在激活 4G 模块（最多等待 90 秒）..."
  : >| "${ACTIVATE_LOG}"
  "${BIN}" -activate >>"${ACTIVATE_LOG}" 2>&1 &
  local apid=$!
  local i=0
  while (( i < 90 )); do
    kill -0 "${apid}" 2>/dev/null || break
    sleep 1
    (( i++ ))
  done
  if kill -0 "${apid}" 2>/dev/null; then
    kill "${apid}" 2>/dev/null || true
    wait "${apid}" 2>/dev/null || true
    note "警告：模块激活超时。"
  else
    wait "${apid}" 2>/dev/null || true
  fi

  if [[ -s "${ACTIVATE_LOG}" ]]; then
    sed 's/^/  /' "${ACTIVATE_LOG}"
  fi

  if has_ecm_ip; then
    note "4G 网卡激活成功。"
    return 0
  fi
  note "警告：4G 网卡未就绪，管理页面仍会启动。详情：${ACTIVATE_LOG}"
  return 1
}

stop_existing() {
  job_loaded && launchctl bootout "${DOMAIN}/${LABEL}" >/dev/null 2>&1
  kill_matching "${BIN}" TERM >/dev/null 2>&1 || true
  kill_matching "${ROOT_DIR}/dist/dj4ghub-macos" TERM >/dev/null 2>&1 || true
  if [[ -s "${PID_FILE}" ]]; then
    local pid
    pid="$(<"${PID_FILE}")"
    pid_alive "${pid}" && kill -TERM "${pid}" 2>/dev/null || true
  fi
  local i=0
  while (( i < 10 )); do
    job_loaded || break
    sleep 0.1
    (( i++ ))
  done
}

start_via_launchd() {
  local err=""
  local attempt=0
  while (( attempt < 5 )); do
    if err="$(launchctl bootstrap "${DOMAIN}" "${PLIST}" 2>&1)"; then
      return 0
    fi
    launchctl bootout "${DOMAIN}/${LABEL}" >/dev/null 2>&1 || true
    sleep 0.5
    (( attempt++ ))
  done
  BOOTSTRAP_ERROR="${err}"
  return 1
}

start_fallback() {
  note "launchd 启动失败${BOOTSTRAP_ERROR:+（${BOOTSTRAP_ERROR}）}，改用后台进程启动..."
  if (( DEMO )); then
    nohup "${BIN}" -demo >>"${SERVICE_LOG}" 2>&1 &
  else
    nohup "${BIN}" >>"${SERVICE_LOG}" 2>&1 &
  fi
  print -r -- "$!" >| "${PID_FILE}"
}

wait_ready() {
  local i=0 pid=""
  while (( i < 60 )); do
    health_ok && return 0
    if (( i == 12 )); then
      [[ -s "${PID_FILE}" ]] && pid="$(<"${PID_FILE}")"
      if ! job_loaded && ! pid_alive "${pid}"; then
        return 1
      fi
    fi
    sleep 0.5
    (( i++ ))
  done
  return 1
}

record_pid() {
  local pid
  pid="$(service_pid)"
  if [[ -z "${pid}" && -s "${PID_FILE}" ]]; then
    pid="$(<"${PID_FILE}")"
  fi
  if [[ -n "${pid}" ]]; then
    print -r -- "${pid}" >| "${PID_FILE}"
  fi
}

show_failure() {
  note ""
  note "启动失败或超时。"
  if job_loaded; then
    note "--- launchd 状态 ---"
    launchctl print "${DOMAIN}/${LABEL}" 2>&1 | sed -n '1,15p'
  fi
  if [[ -f "${SERVICE_LOG}" ]]; then
    note "--- 最近日志（${SERVICE_LOG}）---"
    tail -n 20 "${SERVICE_LOG}" 2>/dev/null
  fi
  launchctl bootout "${DOMAIN}/${LABEL}" >/dev/null 2>&1 || true
  kill_matching "${BIN}" TERM >/dev/null 2>&1 || true
  rm -f "${PID_FILE}"
  pause_and_exit "请根据上面的日志排查后重试。"
}

for arg in "$@"; do
  case "${arg}" in
    --demo)
      DEMO=1
      SKIP_NETWORK=1
      ;;
    --no-network)
      SKIP_NETWORK=1
      ;;
    --no-open)
      OPEN_PAGE=0
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      print -u2 -- "未知参数：${arg}"
      usage
      exit 2
      ;;
  esac
done

APP_BUNDLE="/Applications/DJ 4G Hub.app"
APP_CLIENT="${APP_BUNDLE}/Contents/MacOS/DJ4HubClient"
if (( ! DEMO )) && [[ -z "${DJ4GHUB_SOURCE_BIN:-}" && -x "${APP_CLIENT}" && -x "${APP_BUNDLE}/Contents/MacOS/dj4ghub-macos" ]]; then
  "${APP_CLIENT}" --start-service || exit $?
  open -a "${APP_BUNDLE}" --args --background
  (( OPEN_PAGE )) && open "${URL}"
  exit 0
fi

mkdir -p "${SUPPORT_DIR}" "${BIN_DIR}" "${LOG_DIR}" || pause_and_exit "无法创建数据目录：${SUPPORT_DIR}"

if health_ok; then
  note "DJ 4G Hub 已经在运行。"
  (( OPEN_PAGE )) && open "${URL}" >/dev/null 2>&1
  exit 0
fi

if port_busy; then
  note "端口 7575 已被其他程序占用："
  /usr/sbin/lsof -nP -iTCP:7575 -sTCP:LISTEN 2>/dev/null | sed -n '1,5p'
  pause_and_exit "请先关闭占用 7575 端口的程序。"
fi

install_binary || pause_and_exit "准备可执行文件失败。"
rotate_log

# 释放可能残留的旧实例，避免 USB AT 接口被占用
stop_existing

activate_network || true

write_plist

note "正在启动 DJ 4G Hub..."
if start_via_launchd; then
  :
else
  start_fallback || pause_and_exit "无法启动服务。"
fi

if wait_ready; then
  record_pid
  note "DJ 4G Hub 已启动：${URL}"
  note "日志：${SERVICE_LOG}"
  (( OPEN_PAGE )) && open "${URL}" >/dev/null 2>&1
  exit 0
fi

show_failure
