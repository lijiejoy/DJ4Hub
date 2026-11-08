#!/bin/zsh
#
# DJ 4G Hub 停止脚本（双击运行）
#
# 停止由 start-dj4hub.command 启动的服务，兼容两种启动方式：
#   1. launchd 服务 com.dj4hub.local
#   2. 后台进程（launchd 不可用时的回退方式）
# 同时清理旧版本直接运行 dist/dj4ghub-macos 留下的进程。
#
set -u

# A consolidated installation owns its lifecycle and protects active calls.
APP_CLIENT="/Applications/DJ 4G Hub.app/Contents/MacOS/DJ4HubClient"
APP_HELPER="/Applications/DJ 4G Hub.app/Contents/MacOS/dj4ghub-macos"
if [[ -x "${APP_CLIENT}" && -x "${APP_HELPER}" ]]; then
  exec "${APP_CLIENT}" --stop-service
fi

APP_NAME="DJ 4G Hub"
LABEL="com.dj4hub.local"
ROOT_DIR="${0:A:h}"

SUPPORT_DIR="${HOME}/Library/Application Support/${APP_NAME}"
BIN="${SUPPORT_DIR}/bin/dj4ghub-macos"
LEGACY_BIN="${ROOT_DIR}/dist/dj4ghub-macos"
PID_FILE="${SUPPORT_DIR}/dj4ghub.pid"
DOMAIN="gui/$(id -u)"

job_loaded() {
  launchctl print "${DOMAIN}/${LABEL}" >/dev/null 2>&1
}

pid_alive() {
  [[ -n "${1:-}" ]] && kill -0 "$1" 2>/dev/null
}

is_listening() {
  /usr/sbin/lsof -nP -iTCP:7575 -sTCP:LISTEN >/dev/null 2>&1
}

pids_matching() {
  pgrep -f -- "$1" 2>/dev/null || true
}

has_process() {
  [[ -n "$(pids_matching "${BIN}")" || -n "$(pids_matching "${LEGACY_BIN}")" ]]
}

pause_on_failure() {
  if [[ -t 0 ]]; then
    print -n "按回车关闭此窗口..."
    read -r _
  fi
}

signal_matching() {
  local pattern="$1" signal="$2" pid hit=1
  while read -r pid; do
    [[ -z "${pid}" || "${pid}" == "$$" ]] && continue
    if kill -"${signal}" "${pid}" 2>/dev/null; then
      hit=0
    fi
  done < <(pids_matching "${pattern}")
  return "${hit}"
}

stopped=0

if job_loaded; then
  print -r -- "正在停止 launchd 服务 ${LABEL}..."
  launchctl bootout "${DOMAIN}/${LABEL}" >/dev/null 2>&1 \
    || launchctl kill TERM "${DOMAIN}/${LABEL}" >/dev/null 2>&1 \
    || true
  stopped=1
fi

# 清理回退方式或旧脚本直接启动的进程
signal_matching "${BIN}" TERM && stopped=1
signal_matching "${LEGACY_BIN}" TERM && stopped=1

# 进程名匹配不到时，按 PID 文件兜底
if [[ -s "${PID_FILE}" ]]; then
  pid="$(<"${PID_FILE}")"
  if pid_alive "${pid}" && kill -TERM "${pid}" 2>/dev/null; then
    stopped=1
  fi
fi

# 等待进程退出（最多 8 秒）
i=0
while (( i < 40 )); do
  if ! job_loaded && ! is_listening && ! has_process; then
    break
  fi
  sleep 0.2
  (( i++ ))
done

# 仍未退出则强制结束
if job_loaded || is_listening || has_process; then
  job_loaded && launchctl bootout "${DOMAIN}/${LABEL}" >/dev/null 2>&1
  signal_matching "${BIN}" KILL
  signal_matching "${LEGACY_BIN}" KILL
  if [[ -s "${PID_FILE}" ]]; then
    pid="$(<"${PID_FILE}")"
    pid_alive "${pid}" && kill -KILL "${pid}" 2>/dev/null
  fi
  sleep 0.5
fi

rm -f "${PID_FILE}"

if job_loaded; then
  print -u2 -- "❌ DJ 4G Hub 服务仍在运行，无法卸载（${LABEL}）。"
  launchctl print "${DOMAIN}/${LABEL}" 2>&1 | sed -n '1,10p'
  pause_on_failure
  exit 1
fi

if is_listening; then
  print -u2 -- "❌ 端口 7575 仍在监听，可能是其他程序占用了该端口："
  /usr/sbin/lsof -nP -iTCP:7575 -sTCP:LISTEN 2>&1 | sed -n '1,6p'
  pause_on_failure
  exit 1
fi

if has_process; then
  print -u2 -- "❌ DJ 4G Hub 进程仍未退出："
  pids_matching "${BIN}" | sed -n '1,5p'
  print -u2 -- "可尝试在活动监视器中结束该进程，或重新运行本脚本。"
  pause_on_failure
  exit 1
fi

if (( stopped )); then
  print -r -- "DJ 4G Hub 已停止。"
else
  print -r -- "DJ 4G Hub 当前没有运行。"
fi
