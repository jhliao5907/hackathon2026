#!/bin/sh
# 容器启动脚本：按 sbx 注入的环境变量，为当前 agent 生成 MCP 配置。
# 配置里只写宿主机 broker 的 URL，绝不含任何密钥 —— 这正是「MCP 配置零敏感信息」。
set -e

: "${SBX_BROKER_BASE:=http://host.docker.internal:8771}"
: "${SBX_AGENT:=claude}"

# 为 Claude Code 注册 MCP server（HTTP 传输，指向 broker，无密钥）。
register_mcp_claude() {
  for s in $(echo "$1" | tr ',' ' '); do
    url="$SBX_BROKER_BASE/$s"
    echo "[sbx-entrypoint] (claude) 注册 MCP '$s' -> $url （无密钥）"
    claude mcp add --scope user --transport http "$s" "$url" >/dev/null 2>&1 \
      || echo "[sbx-entrypoint] 跳过 '$s'（claude 未就绪或已存在）"
  done
}

# 为 Codex 注册 MCP server（url 键即 Streamable HTTP，指向 broker，无密钥）。
register_mcp_codex() {
  for s in $(echo "$1" | tr ',' ' '); do
    url="$SBX_BROKER_BASE/$s"
    echo "[sbx-entrypoint] (codex) 注册 MCP '$s' -> $url （无密钥）"
    codex mcp add "$s" --url "$url" >/dev/null 2>&1 \
      || echo "[sbx-entrypoint] 跳过 '$s'（codex 未就绪或已存在）"
  done
}

# 为 Codex 写自定义模型 provider：base_url 指向宿主机注入代理，
# 容器内只放占位 key（OPENAI_API_KEY），真实 key 由代理注入。模型 key 不落沙箱。
write_codex_provider() {
  [ -n "$SBX_INJECTOR_BASE" ] || return 0
  mkdir -p "$HOME/.codex"
  model_line=""
  [ -n "$SBX_CODEX_MODEL" ] && model_line="model = \"$SBX_CODEX_MODEL\""
  cat > "$HOME/.codex/config.toml" <<EOF
$model_line
model_provider = "sbx"

[model_providers.sbx]
name = "sbx"
base_url = "$SBX_INJECTOR_BASE/openai/v1"
wire_api = "responses"
env_key = "OPENAI_API_KEY"
EOF
  echo "[sbx-entrypoint] (codex) 模型 provider -> $SBX_INJECTOR_BASE/openai/v1 （真实 key 由宿主机注入）"
}

case "$SBX_AGENT" in
  claude)
    [ -n "$SBX_MCP_SERVERS" ] && register_mcp_claude "$SBX_MCP_SERVERS"
    ;;
  codex)
    write_codex_provider
    [ -n "$SBX_MCP_SERVERS" ] && register_mcp_codex "$SBX_MCP_SERVERS"
    ;;
  *)
    echo "[sbx-entrypoint] agent='$SBX_AGENT' 暂未内置自动配置，跳过 MCP/provider 注入"
    ;;
esac

# 没有参数时给个交互 shell，便于手动验证隔离/出网。
if [ "$#" -eq 0 ]; then
  exec /bin/sh
fi
exec "$@"
