#!/bin/sh
# 容器启动脚本：按 sbx 注入的环境变量生成 MCP 配置。
# 配置里只写宿主机 broker 的 URL，绝不含任何密钥 —— 这正是「MCP 配置零敏感信息」。
set -e

: "${SBX_BROKER_BASE:=http://host.docker.internal:8771}"

if [ -n "$SBX_MCP_SERVERS" ]; then
  OLD_IFS="$IFS"
  IFS=','
  for s in $SBX_MCP_SERVERS; do
    [ -z "$s" ] && continue
    url="$SBX_BROKER_BASE/$s"
    echo "[sbx-entrypoint] 注册 MCP server '$s' -> $url （无密钥）"
    # 以 HTTP 传输把 MCP server 指向 broker；用户级作用域，避免写 /workspace。
    claude mcp add --scope user --transport http "$s" "$url" >/dev/null 2>&1 \
      || echo "[sbx-entrypoint] 提示：claude 未就绪或已存在，跳过 '$s' 的自动注册"
  done
  IFS="$OLD_IFS"
fi

# 没有参数时给个交互 shell，便于 demo 里手动验证隔离/出网。
if [ "$#" -eq 0 ]; then
  exec /bin/sh
fi
exec "$@"
