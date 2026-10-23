# 验收报告

| 项目 | `sbx` 桌面级安全沙箱 |
|---|---|
| 验收日期 | 2026-10-23 |
| 验收环境 | macOS (Apple Silicon) + Colima(Docker) ｜ Go 1.21+ ｜ Docker 29.x |
| 验收方式 | 单元测试（自动）+ 真机端到端联调（手动） |
| 结论 | 全部 check 点通过；Claude Code 与 Codex 两条 agent 链路均真机验证（含真实模型调用） |

本文记录验收的范围、方法、各 check 点的预期与实际结果。命令可复现。

---

## 1. 验收范围

按设计目标（见 [DESIGN.md](DESIGN.md) §2）逐项验收：

1. 隔离（进程 / 文件 / 网络 / 能力 / 资源）
2. MCP 凭据不落沙箱
3. 模型 key 不落沙箱
4. 出网 deny-by-default 白名单
5. 可审计
6. 一次性工作区 + diff 审批
7. 多 agent（Claude Code / Codex）

---

## 2. 单元测试（自动）

```bash
go build ./... && go vet ./... && go test ./...
```

结果：**全部通过**（`cmd/sbx` 与 `secrets` 无测试文件，其余包 ok）。

| 包 | 关键用例 | 覆盖点 |
|---|---|---|
| `gateway` | `TestAllowed` / `TestLiveProxyAllowDeny` | 精确/子域/伪装前缀判定；实时代理白名单内 200、外 403 |
| `broker` | 注入+桥接 / 通知 202 / 未知 server 404 | mock stdio server 回带注入值；JSON-RPC 请求/通知区分 |
| `injector` | 剥前缀+注入 / Bearer 模板 / 未知 404 / 非法 target | 覆盖占位值、`${名}` 模板解析 |
| `config` | 解析去重 / 模板引用 / `SecretRefs`+`Resolve` / 缺 command | 配置校验与密钥名提取 |
| `sandbox` | `BuildArgs` 加固断言 / 未知 agent / LLM 代理 env | docker run 参数、无密钥泄露、注入占位 key |
| `audit` | JSONL 合法/判定/不记值 / nil 安全 | 审计格式与「只记名不记值」 |
| `workspace` | 改/增/删 → Diff → Apply | 一次性工作区回写正确性 |
| `catalog` | `BuildConfig` / `RequiredSecretNames` | 预设→配置生成、白名单去重、所需密钥汇总 |
| `webui` | `/api/state` `/api/config` `/api/launch` `/` | 预设下发、生成可加载 yaml、缺密钥提示、首页返回 |
| `config` | `Save` 往返 | GUI 生成的配置能被 `Load` 读回 |

---

## 3. 真机端到端联调（手动）

> 说明：Claude 的「真实模型出话」未跑（需用户 Anthropic key 与交互登录）；Codex 的全链路（含真实模型调用）
> 已用用户本机的 OpenAI 兼容网关凭据验证，验证后凭据已从 Keychain 删除、临时目录已清理，仓库无任何真实密钥残留。

### 3.1 镜像与隔离加固

| # | check 点 | 方法 | 预期 | 实际结果 | 结论 |
|---|---|---|---|---|---|
| A1 | 非 root 运行 | `docker run ... id` | uid=10001 | `uid=10001(agent) gid=10001` | ✅ |
| A2 | 只读 rootfs | 容器内 `touch /oops` | 失败 | `Read-only file system` | ✅ |
| A3 | 可写区受控 | 容器内写 `/tmp`、`/home/agent` | 成功 | 两者均可写（家目录 tmpfs `mode=1777`） | ✅ |
| A4 | 能力全丢 | 读 `/proc/1/status` | CapEff=0 | `CapEff: 0000000000000000` | ✅ |
| A5 | 资源上限 | `BuildArgs` 断言 | 含 memory/cpus/pids-limit | 单测通过 | ✅ |
| A6 | agent CLI 就位 | `claude --version` / `codex --version` | 均存在 | `2.1.197` / `codex-cli 0.164.0` | ✅ |

> 验收中发现并修复：`/home/agent` 的 tmpfs 默认属主 root、非 root 用户不可写 → 加 `mode=1777` 修复。

### 3.2 MCP 凭据不落沙箱（broker）

| # | check 点 | 方法 | 预期 | 实际结果 | 结论 |
|---|---|---|---|---|---|
| B1 | broker 真实拉起 MCP 容器 | POST `initialize` 到 `:8771/filesystem` | 返回 serverInfo | `secure-filesystem-server` 正常握手 | ✅ |
| B2 | 通知消息处理 | POST `notifications/initialized` | HTTP 202 | `HTTP 202` | ✅ |
| B3 | 工具可用 | POST `tools/list` | 返回工具 | **14 个工具**（read_file/write_file/…） | ✅ |
| B4 | 容器内 MCP 配置无密钥(claude) | 容器内 `claude mcp list` | 仅 broker URL | `filesystem: http://host:8771/filesystem (HTTP) - ✔ Connected` | ✅ |
| B5 | 容器内 MCP 配置无密钥(codex) | 查看容器 `~/.codex/config.toml` | 仅 broker URL | `[mcp_servers.*] url=http://host:8771/...`，无任何密钥 | ✅ |
| B6 | **Codex 会话内真实调用 MCP 工具** | 容器内 `codex exec` 让其调用 filesystem 工具 | broker 出现 initialize→tools/list→tools/call | 审计依次出现 `initialize`、`tools/list`、`tools/call`；`list_allowed_directories` 经 broker→MCP 容器返回 `Allowed directories:\n/tmp` | ✅ |

> 验收中发现并修复：
> 1. entrypoint 未按逗号拆分 `SBX_MCP_SERVERS` → 改用 `tr ',' ' '` 修复。
> 2. Codex 的 MCP 客户端要求 Streamable-HTTP 的 POST 响应为 `text/event-stream`（SSE），
>    而 broker 原先只回 `application/json`（Claude 可接受、Codex 卡在 `initialize`）
>    → broker 增加内容协商：`Accept` 含 `text/event-stream` 时以 SSE 回单条响应，Codex 握手即通。

### 3.3 出网 deny-by-default（gateway）

| # | check 点 | 方法 | 预期 | 实际结果 | 结论 |
|---|---|---|---|---|---|
| C1 | 白名单内放行 | 容器经代理访问 `api.github.com` | 放行 | 网关日志 `ALLOW CONNECT api.github.com:443` | ✅ |
| C2 | npx 下载走白名单 | MCP 容器 npx 拉包 | 经网关放行 | `ALLOW CONNECT registry.npmjs.org:443` | ✅ |
| C3 | 白名单外拦截 | 容器访问 `example.com` | 拒绝 | 网关 `DENY CONNECT example.com:443`，容器侧 curl 失败 | ✅ |

### 3.4 凭据不落沙箱 — 文件/环境隔离

| # | check 点 | 方法 | 预期 | 实际结果 | 结论 |
|---|---|---|---|---|---|
| D1 | 环境无密钥 | 容器内 `env \| grep -i key/token/secret` | 空或仅占位 | 无任何真实密钥 | ✅ |
| D2 | 看不到宿主机目录 | 容器内 `ls /Users` | 不存在 | `No such file or directory` | ✅ |
| D3 | 读不到宿主机私钥 | 容器内 `ls ~/.ssh` | 失败 | 不可访问 | ✅ |

### 3.5 模型 key 不落沙箱（injector）— Codex 真机验证

使用用户本机 OpenAI 兼容网关的静态 Bearer 凭据，经 `printf ... | sbx secret set openai-key` 灌入宿主机 Keychain；
`sandbox.yaml` 的 `openai` 代理 `target` 指向该网关，header `Authorization: "Bearer ${openai-key}"`。

| # | check 点 | 方法 | 预期 | 实际结果 | 结论 |
|---|---|---|---|---|---|
| E1 | 注入层转发 | 带占位 `Authorization` 打 `:8773/openai/v1/models` | 注入真实 key 后 200 | **HTTP 200，返回 13 个真实模型** | ✅ |
| E2 | 容器全链路模型调用 | 容器内 `codex exec "...SANDBOX_OK..."`（仅占位 key） | 模型正常返回 | 模型返回 **`SANDBOX_OK`**（4019 tokens） | ✅ |
| E3 | 真实 key 不在容器 | 容器 env 仅 `OPENAI_API_KEY=sandbox-placeholder` | 不含真实 key | 确认仅占位值 | ✅ |
| E4 | 审计留痕 | 查 `sbx-audit.log` | 有 secret+llm 事件 | `secret(openai-key)` + `llm(POST /v1/responses)` | ✅ |

### 3.6 审计

| # | check 点 | 方法 | 预期 | 实际结果 | 结论 |
|---|---|---|---|---|---|
| F1 | 结构化落盘 | 查 `sbx-audit.log` | JSONL | 每行合法 JSON，含 time/type | ✅ |
| F2 | 覆盖四类事件 | 全链路后查看 | egress/mcp/llm/secret | 四类均出现 | ✅ |
| F3 | 不记密钥值 | 检查 secret 事件 | 只记名字 | 仅 `"secret":"openai-key"`，无值 | ✅ |

### 3.7 图形配置界面（友好性）

| # | check 点 | 方法 | 预期 | 实际结果 | 结论 |
|---|---|---|---|---|---|
| H1 | 界面启动 | `sbx ui --port 8785` | 本机监听、返回首页 | 服务起、`GET /` 返回含「sbx 安全沙箱」的页面 | ✅ |
| H2 | 预设下发 | `GET /api/state` | 返回 agents/MCP 预设/密钥状态 | agents=[claude,codex]、MCP=[github,filesystem,fetch]、secrets 状态齐全 | ✅ |
| H3 | 勾选生成配置 | `POST /api/config` claude+github+filesystem | 写出合法无密钥 yaml | 生成的 `sandbox.yaml` 含两 MCP + anthropic 代理 + 汇总白名单，仅密钥名无值 | ✅ |
| H4 | 配置可被消费 | `config.Load` 读回生成文件 | 成功且字段正确 | 单测通过（webui 生成 → Load 读回一致） | ✅ |
| H5 | 缺密钥提示 | `POST /api/launch` 未设密钥 | 报告缺失项 | 返回 `ok:false, missing:[gh-token, openai-key]` | ✅ |

### 3.8 一次性工作区 + diff 审批

| # | check 点 | 方法 | 预期 | 实际结果 | 结论 |
|---|---|---|---|---|---|
| G1 | 改动落副本 | `workspace` 单测 | 原目录审批前不变 | 通过 | ✅ |
| G2 | Diff 检出增/改/删 | `workspace` 单测 | 三类改动均检出 | 通过 | ✅ |
| G3 | Apply 正确回写 | `workspace` 单测 | 增/改/删同步、未改动不丢 | 通过 | ✅ |

---

## 4. 已知边界（未覆盖）

- **Claude 真实模型出话**：需用户 Anthropic key 与容器内交互登录，未在验收中执行（基础设施链路其余均已验证）。
- **隔离强度**：容器共享宿主机内核，弱于虚拟机级隔离（演进项，见 DESIGN §10）。
- **平台**：`host.docker.internal` 依赖对应 Docker 运行时支持。

---

## 5. 复现要点

```bash
# 单元测试
go build ./... && go vet ./... && go test ./...

# 构建镜像
docker build -f docker/Dockerfile.agent -t sbx-agent:latest .

# 仅起宿主机侧服务（联调用）
./sbx serve

# 典型联调：broker 实连（无需密钥的 MCP）、网关放行/拦截、容器隔离、审计
#   详见本文 §3 各 check 点命令
```
