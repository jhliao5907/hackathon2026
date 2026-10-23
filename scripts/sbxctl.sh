#!/usr/bin/env bash
# sbx 本地服务一键启停脚本。
#
#   ./scripts/sbxctl.sh start     启动本地服务（后端 serve + 配置界面 ui）
#   ./scripts/sbxctl.sh stop      停止本地服务
#   ./scripts/sbxctl.sh restart   重启
#   ./scripts/sbxctl.sh status    查看状态
#   ./scripts/sbxctl.sh logs      跟踪日志
#
# 选项：
#   start --no-ui / --no-serve    只起其中一个
#   start --no-open               不自动打开浏览器
#   stop  --stop-docker           连同 Colima(Docker) 一起停
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
RUN="$ROOT/.sbx-run"
BIN="$ROOT/sbx"
UI_PORT=8780
BROKER=8771; GATEWAY=8772; INJECTOR=8773

mkdir -p "$RUN"

c_g(){ printf '\033[32m%s\033[0m\n' "$*"; }   # 绿
c_y(){ printf '\033[33m%s\033[0m\n' "$*"; }   # 黄
c_r(){ printf '\033[31m%s\033[0m\n' "$*"; }   # 红
c_d(){ printf '\033[90m%s\033[0m\n' "$*"; }   # 灰

# 构建二进制（缺失或源码更新时）。
ensure_bin(){
  if [ ! -x "$BIN" ] || [ -n "$(find cmd internal -name '*.go' -newer "$BIN" 2>/dev/null | head -1)" ]; then
    c_d "编译 sbx ..."
    go build -o "$BIN" ./cmd/sbx
  fi
}

# 确保 Docker 可用（Colima 已装但未起则自动拉起）。
ensure_docker(){
  if docker info >/dev/null 2>&1; then return; fi
  if command -v colima >/dev/null 2>&1; then
    c_y "Docker 未就绪，正在启动 Colima ..."
    colima start >/dev/null 2>&1 || { c_r "Colima 启动失败，请手动 colima start"; return 1; }
    c_g "Colima 已启动"
  else
    c_r "Docker 守护进程未运行，请先启动 Docker"; return 1
  fi
}

# 确保有配置文件（serve 需要读取）。
ensure_config(){
  [ -f "$ROOT/sandbox.yaml" ] || { c_d "生成默认 sandbox.yaml ..."; "$BIN" init >/dev/null; }
}

pidfile(){ echo "$RUN/$1.pid"; }
logfile(){ echo "$RUN/$1.log"; }

running(){ # running <name>
  local pf; pf="$(pidfile "$1")"
  [ -f "$pf" ] && kill -0 "$(cat "$pf")" 2>/dev/null
}

start_one(){ # start_one <name> <cmd...>
  local name="$1"; shift
  if running "$name"; then c_d "$name 已在运行 (pid $(cat "$(pidfile "$name")"))"; return; fi
  nohup "$@" >"$(logfile "$name")" 2>&1 &
  echo $! >"$(pidfile "$name")"
  sleep 0.4
  if running "$name"; then c_g "$name 已启动 (pid $!)"; else c_r "$name 启动失败，见 $(logfile "$name")"; fi
}

stop_one(){ # stop_one <name>
  local pf; pf="$(pidfile "$1")"
  if [ -f "$pf" ] && kill -0 "$(cat "$pf")" 2>/dev/null; then
    kill "$(cat "$pf")" 2>/dev/null || true
    c_g "$1 已停止"
  else
    c_d "$1 未在运行"
  fi
  rm -f "$pf"
}

open_browser(){ command -v open >/dev/null 2>&1 && open "http://127.0.0.1:$UI_PORT" >/dev/null 2>&1 || true; }

cmd_start(){
  local with_ui=1 with_serve=1 do_open=1
  for a in "$@"; do case "$a" in
    --no-ui) with_ui=0;; --no-serve) with_serve=0;; --no-open) do_open=0;;
  esac; done

  ensure_bin
  ensure_docker || exit 1
  ensure_config

  [ "$with_serve" = 1 ] && start_one serve "$BIN" serve
  [ "$with_ui" = 1 ] && start_one ui "$BIN" ui --port "$UI_PORT" --no-open

  echo
  [ "$with_serve" = 1 ] && c_d "后端服务：broker :$BROKER  gateway :$GATEWAY  injector :$INJECTOR"
  if [ "$with_ui" = 1 ]; then
    c_g "配置界面：http://127.0.0.1:$UI_PORT"
    [ "$do_open" = 1 ] && open_browser
  fi
}

cmd_stop(){
  stop_one ui
  stop_one serve
  # 清理可能残留的 MCP server 容器（broker 以 --rm 启动，多数会自行退出）。
  docker ps -q --filter ancestor=node:20-slim 2>/dev/null | xargs -r docker rm -f >/dev/null 2>&1 || true
  for a in "$@"; do if [ "$a" = "--stop-docker" ] && command -v colima >/dev/null 2>&1; then
    c_y "停止 Colima ..."; colima stop >/dev/null 2>&1 || true; c_g "Colima 已停止"
  fi; done
}

port_state(){ # port_state <port>
  if lsof -iTCP:"$1" -sTCP:LISTEN -n >/dev/null 2>&1; then c_g "  :$1  LISTEN"; else c_d "  :$1  —"; fi
}

cmd_status(){
  echo "进程："
  for n in serve ui; do
    if running "$n"; then c_g "  $n  运行中 (pid $(cat "$(pidfile "$n")"))"; else c_d "  $n  未运行"; fi
  done
  echo "端口："
  for p in $BROKER $GATEWAY $INJECTOR $UI_PORT; do port_state "$p"; done
  echo "Docker："
  if docker info >/dev/null 2>&1; then c_g "  可用"; else c_r "  不可用"; fi
}

cmd_logs(){ tail -n 40 -F "$RUN"/serve.log "$RUN"/ui.log 2>/dev/null; }

case "${1:-}" in
  start)   shift; cmd_start "$@";;
  stop)    shift; cmd_stop "$@";;
  restart) shift; cmd_stop; sleep 1; cmd_start "$@";;
  status)  cmd_status;;
  logs)    cmd_logs;;
  *) echo "用法: $0 {start|stop|restart|status|logs} [选项]"; exit 1;;
esac
