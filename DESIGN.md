# 桌面级安全沙箱 — 设计文档

| 项目代号 | `sbx` |
|---|---|
| 文档版本 | v1.0 |
| 更新日期 | 2026-10-23 |
| 形态 | 可运行原型（Prototype） |
| 实现语言 | Go |
| 隔离后端 | Docker 容器 |

## 目录

1. [概述](#1-概述)
2. [设计目标](#2-设计目标)
3. [设计原则与技术选型](#3-设计原则与技术选型)
4. [总体架构](#4-总体架构)
5. [核心机制](#5-核心机制)
6. [模块设计](#6-模块设计)
7. [关键数据流](#7-关键数据流)
8. [安全模型](#8-安全模型)
9. [验证](#9-验证)
10. [演进路线](#10-演进路线)

---

## 1. 概述

AI agent（Claude Code、Codex、dsh 等）直接运行在开发者机器上时，默认拥有**与登录用户同等的权限**。
一次提示注入、一个被投毒的依赖、一次模型幻觉，都可能导致读取 `~/.ssh`、窃取 API key、
或把代码推送到外部。与此同时，为 agent 接入各类 MCP server 往往要求在配置中填入
token / password，使敏感信息散落在多处明文文件里。

`sbx` 是一个**桌面级安全沙箱**：它把 agent 关进一个受控的隔离环境，
在保证安全的前提下尽量不改变用户的使用习惯，并从机制上保证
**在沙箱内配置任何 MCP 时都不需要填写任何敏感信息**。

---

## 2. 设计目标

优先级从高到低：

| 优先级 | 目标 | 含义 | 验收标准 |
|---|---|---|---|
| P0 | 隔离 | agent 的进程、文件、网络与宿主机隔离 | 容器非 root 运行、看不到宿主机密钥文件、无直连公网 |
| P0 | 凭据不落沙箱 | 所有密钥只存在于宿主机，沙箱内只见无密钥入口 | 容器内 `env` 与 MCP 配置查不到任何真实 key |
| P1 | 出网可控 | 默认拒绝，仅白名单域名放行 | 白名单内连通、白名单外被拦截并留痕 |
| P1 | 可审计 | 关键行为全程留痕 | 出网 / MCP 调用 / 凭据注入均有结构化日志 |
| P2 | 易用 | 终端用户操作最少 | 存一次密钥 + 一条命令启动 |
| P2 | 多 agent | 支持主流 agent | 统一入口参数化启动；Claude Code / Codex 均打通 MCP 零密钥接入 |

**非目标**：不自研 agent 或大模型；不追求生产级多租户；当前原型聚焦单机单用户场景。

---

## 3. 设计原则与技术选型

### 3.1 核心设计原则

1. **信任边界清晰**：宿主机为可信侧，沙箱内 agent 为**不可信工作负载**。所有敏感物（密钥、
   宿主机文件系统、公网出口）都留在可信侧，沙箱只通过受控接口访问。
2. **凭据与使用者分离**：agent 需要「能力」（调用某个 MCP、调用模型），但不需要「凭据」本身。
   凭据由可信侧在调用发生时注入，沙箱永远拿不到明文。
3. **默认拒绝（deny-by-default）**：出网、能力、文件可见性都从「全部关闭」开始，仅按需开最小集合。
4. **可观测**：安全不仅是阻断，还要可追溯——关键行为必须留痕。
5. **对用户透明**：安全机制尽量不侵入 agent 的正常工作流，配置里不出现技术化、易错的敏感字段。

### 3.2 隔离后端：容器

隔离后端在「虚拟机级隔离」与「容器级隔离」之间权衡：

| 维度 | 容器 | 虚拟机级隔离 |
|---|---|---|
| 隔离强度 | 共享内核，依赖 namespace/cgroup/capability | 独立内核，强度更高 |
| 启动开销 | 秒级、轻 | 较重 |
| 跨平台与易用 | 成熟、依赖少 | 配置复杂、平台受限 |

当前原型选择 **容器**：在桌面单机场景下，配合 `cap-drop ALL`、`no-new-privileges`、
只读 rootfs、非 root 用户与资源上限，已能覆盖主要威胁，且启动轻、跨平台、依赖少。
隔离后端被设计为**可替换**，未来可在不改动上层的前提下换成更强的虚拟机级隔离（见[§10](#10-演进路线)）。

### 3.3 凭据层：宿主机密钥库 + 运行时注入

密钥统一存入**操作系统密钥库**（macOS Keychain / Linux Secret Service / Windows Credential Manager），
由可信侧在调用发生时注入到目标进程环境或请求头。配置文件只保存「密钥名」而非密钥本身。
这样密钥既不进配置、也不进镜像、更不进沙箱。

### 3.4 实现语言：Go

单二进制、交叉编译、并发模型契合「同时跑多个本地代理 + 拉起容器」的编排需求，部署无运行时依赖。

---

## 4. 总体架构

### 4.1 一句话概括

> agent 运行在隔离容器中，其 MCP 配置只指向宿主机 **broker** 的本地 URL（零密钥）；
> 连 agent 自身的模型 key 也不进沙箱（容器内只放占位值，真实 key 由宿主机 **injector** 注入）；
> 真实凭据保存在宿主机**密钥库**，由 broker / injector 在容器外注入；
> 真实 MCP server 本身也运行在受限容器中；容器的一切出网经**白名单网关**（deny-by-default）；
> 所有关键行为写入**审计日志**。

### 4.2 宿主机侧常驻服务

| 服务 | 端口 | 职责 |
|---|---|---|
| broker | 8771 | MCP 凭据代理：HTTP ↔ stdio 桥接，向 MCP server 注入密钥 |
| gateway | 8772 | 出网白名单正向代理（deny-by-default） |
| injector | 8773 | 凭据注入反向代理：模型 key 不落沙箱 |

### 4.3 架构图

```
宿主机（可信侧）                                 隔离容器（不可信侧）
┌──────────────────────────────┐              ┌─────────────────────────────┐
│ sbx CLI (cobra)              │   docker run │  claude code / codex / dsh  │
│                              │─────────────▶│                             │
│ ┌──────────────┐             │              │  MCP 配置(容器内):          │
│ │ 系统密钥库   │ 存密钥      │              │   url: http://host:8771/<svc>
│ └──────┬───────┘             │              │   —— 无 key/password ——     │
│        │ 注入               │   HTTP/JSON  │                             │
│ ┌──────▼───────┐  拉起真实   │◀─ -RPC ──────│  模型调用 → injector :8773  │
│ │ broker :8771 │  MCP server │              │  其余出网 → gateway  :8772  │
│ │ (HTTP↔stdio) │  (受限容器) │              └─────────────┬───────────────┘
│ └──────────────┘             │                            │ 所有出网
│ ┌──────────────┐ ┌─────────┐ │              ┌─────────────▼───────────────┐
│ │ injector:8773│ │sandbox. │ │◀── 所有出网 ─│ gateway :8772                │
│ │ 注入模型 key │ │yaml     │ │              │ 域名白名单 deny-by-default   │
│ └──────────────┘ └─────────┘ │              └─────────────────────────────┘
└──────────────────────────────┘
```

容器通过 `host.docker.internal`（`--add-host host.docker.internal:host-gateway`）访问宿主机服务；
容器自身不具备直连公网的能力，一切出网必须经网关按白名单放行。

---

## 5. 核心机制

本节说明三个最能体现安全主张的机制。

### 5.1 MCP 凭据不落沙箱

沙箱内 agent 的 MCP 配置只写一个本地 URL（如 `http://host:8771/github`），不含任何凭据。
真实的 MCP server 由宿主机 broker 在容器外拉起，并在拉起时从密钥库取出密钥注入其环境。
agent 与真实 MCP server 之间的所有 JSON-RPC 流量经 broker 中转。
**密钥只存在于 MCP server 进程，agent 全程只看到一个无密钥的 URL。**

### 5.2 模型 key 不落沙箱

agent 自身调用大模型也需要 key。容器内只配置一个指向宿主机 injector 的 `BASE_URL` 和一个**占位 key**。
agent 的模型请求先到达 injector，由 injector 从密钥库取出真实 key **覆盖写入请求头**后转发至真实上游。
容器内始终只有占位值，真实 key 只存在于宿主机 injector 进程。

### 5.3 默认拒绝的出网 + 全程审计

容器通过 `HTTP_PROXY/HTTPS_PROXY` 被强制将所有出网导向宿主机网关；网关对目标域名做白名单校验，
命中放行、未命中拒绝，并将每一次放行/拒绝连同 MCP 调用、凭据注入一起写入结构化审计日志。

---

## 6. 模块设计

```
cmd/sbx/main.go          CLI 入口 (cobra)
internal/config/         解析 sandbox.yaml（无敏感值）
internal/secrets/        系统密钥库读写
internal/broker/         MCP 凭据代理：HTTP↔stdio 桥接 + 密钥注入 + MCP server 容器化
internal/injector/       凭据注入反向代理：模型 key 不落沙箱
internal/gateway/        出网白名单正向代理（deny-by-default）
internal/audit/          结构化审计日志（JSONL）
internal/workspace/      一次性工作区：快照 / diff / 审批回写
internal/sandbox/        组装 docker run 启动隔离容器（加固）
internal/catalog/        预设目录 + 配置生成（供 GUI）
internal/webui/          本地图形配置界面（单页 Web + JSON API）
docker/                  agent 容器镜像 + 启动脚本
```

### 6.11 catalog + webui — 图形配置界面（友好性）

为「最终用户友好、不要太技术化配置」而设：把常见 MCP server 与各 agent 的模型代理收进
`internal/catalog` 预设目录（技术细节的单一事实源），再由 `internal/webui` 提供一个仅监听
`127.0.0.1` 的单页界面。用户勾选 MCP、填密钥（存入系统钥匙串）、点启动即可，`catalog.BuildConfig`
自动生成 `sandbox.yaml`（汇总白名单、写入 MCP 与代理、仅含密钥名）。页面经 `//go:embed` 内嵌进二进制，
`sbx ui` 一条命令打开。启动按钮拉起终端运行既有的 `sbx run`，不重复管理宿主机服务。

### 6.1 config — 配置（无敏感信息）

- `sandbox.yaml` 包含：`allowlist`（出网白名单域名）、`mcpServers`（启动命令 + 密钥**名**映射）、
  `llmProxies`（模型代理的上游与注入头 + 密钥**名**）。
- 关键：所有 `secrets` / `headers` 的值都是**密钥名**，不是密钥本身。
- `SecretNames()` 汇总去重所有被引用的密钥名，供 CLI 做「启动前就绪检查」。

```yaml
mcpServers:
  github:
    image: "node:20-slim"
    command: ["npx", "-y", "@modelcontextprotocol/server-github"]
    secrets:
      GITHUB_PERSONAL_ACCESS_TOKEN: gh-token   # 环境变量名 : 密钥库中的密钥名
```

### 6.2 secrets — 宿主机密钥库

- 对接操作系统密钥库（macOS Keychain / Linux Secret Service / Windows Credential Manager）。
- 命名空间 `service = "desktop-sandbox"`，提供 `Set / Get / Delete / Has`。
- 密钥**永不写入文件、永不进镜像、永不进容器**。

### 6.3 broker — MCP 凭据代理（核心）

对沙箱以 **MCP Streamable-HTTP** 传输暴露 `/<serverName>` 端点：

1. 容器内 agent 将 JSON-RPC 请求 POST 到 `http://host:8771/<server>`。
2. broker 懒加载对应的真实 MCP server，启动时从密钥库取密钥注入其环境。
3. broker 在 HTTP(JSON-RPC) 与 MCP server 的 stdio(JSON-RPC) 之间双向桥接：
   - **请求**（带 `id`）→ 写入 stdin，等待对应 `id` 的响应，返回 `application/json`。
   - **通知**（无 `id`）→ 写入后直接回 `202 Accepted`，不等待。
   - 单读协程按 `id` 分发响应；server→client 通知当前丢弃。
   - `GET` 返回 `405`（当前不提供 server→client 的 SSE 流，为规范允许的降级）。
   - **内容协商**：POST 响应按请求 `Accept` 回 `application/json` 或 `text/event-stream`（SSE）。
     规范允许两者，但部分客户端（如 Codex 的 rmcp）要求 SSE，故 `Accept` 含 `text/event-stream` 时以 SSE 回单条响应。

**MCP server 容器化**：当 `image` 非空时，真实 MCP server 运行在 `docker run -i` 起的受限容器中
（`--cap-drop ALL`、`no-new-privileges`、出网走网关）。密钥通过 `-e NAME`（不带值）透传——
值只存在于 docker CLI 进程环境，不进入 argv，避免被宿主机 `ps` 观测。`image` 为空时退回宿主机进程模式。

**安全性关键**：真实密钥只注入到 MCP server 侧，agent 全程只看到一个本地 URL。

为可测试性，broker 依赖 `SecretGetter` 接口（生产用密钥库实现，测试注入假实现）。

### 6.4 injector — 凭据注入反向代理

让 agent 自身的模型 key 也不进容器。

- 容器内配置 `ANTHROPIC_BASE_URL=http://host:8773/anthropic` 与一个**占位 key**。
- 模型请求到达 injector 后，`httputil.ReverseProxy` 剥掉 `/anthropic` 前缀、从密钥库取真实 key
  **覆盖写入** `x-api-key` 头，再转发到真实上游。
- 相比 TLS 中间人方案，**无需在容器内安装 CA 证书**，更简单稳定。

### 6.5 gateway — 出网白名单网关

- HTTP 正向代理，支持 `CONNECT`（HTTPS 隧道）与普通 HTTP。
- 对目标 host 做白名单校验：**精确匹配**或**子域名匹配**（`api.github.com` 命中 `github.com`）；
  命中放行并记 `ALLOW`，否则返回 `403` 并记 `DENY`。
- 容器经 `HTTP_PROXY/HTTPS_PROXY` 强制走网关；`NO_PROXY=host.docker.internal` 保证对 broker 的访问不被代理。

### 6.6 audit — 审计日志

- 以 JSON Lines 追加写入 `./sbx-audit.log`，并发安全。
- 事件类型：`egress`（出网 allow/deny）、`mcp`（JSON-RPC 请求）、`llm`（模型调用）、`secret`（密钥读取）。
- 原则：**密钥只记名字、绝不记值**。broker / gateway / injector 均接入。

### 6.7 sandbox — 容器启动器

把安全约束落实到 `docker run` 参数（`BuildArgs` 抽出便于审阅与单测）：

| 约束 | 参数 |
|---|---|
| 非 root | `--user 10001:10001` |
| 丢弃能力 | `--cap-drop ALL` |
| 禁止提权 | `--security-opt no-new-privileges` |
| 只读 rootfs | `--read-only` + `--tmpfs /tmp` + `--tmpfs /home/agent`（`mode=1777`） |
| 资源上限 | `--memory 2g --cpus 2 --pids-limit 512` |
| 文件最小可见 | `-v <工作区>:/workspace -w /workspace`（只挂一个目录） |
| 出网走网关 | `-e HTTP_PROXY/HTTPS_PROXY=http://host:8772` |
| 访问宿主机 | `--add-host host.docker.internal:host-gateway` |
| 无密钥的 MCP 入口 | `-e SBX_BROKER_BASE=... -e SBX_MCP_SERVERS=...` |

### 6.8 workspace — 一次性工作区 + diff 审批

不把真实项目目录直接读写挂进容器，而是先复制一份临时副本挂进去：

1. `Prepare(src)`：复制项目到临时目录，返回快照。
2. 容器挂载**副本**为 `/workspace`，agent 的改动只落在副本。
3. 容器退出后 `Diff()` 展示副本相对原目录的改动。
4. 用户确认后 `Apply()` 将改动镜像回原目录（含新增/修改/删除）；否则丢弃。
5. `Cleanup()` 删除副本。

效果：**只有经用户 diff 审批通过的改动才会离开沙箱**。`--ephemeral=false` 退回直接挂载，`--yes` 免确认。

### 6.9 cmd/sbx — 终端用户 CLI

```
sbx init                 # 生成示例 sandbox.yaml（无密钥）
sbx secret set gh-token  # 交互式存密钥到系统密钥库（不回显）
sbx secret list|rm       # 查看引用密钥是否就绪 / 删除
sbx run claude           # 启动宿主机侧服务，再拉起隔离容器运行 agent
sbx serve                # 仅启动宿主机侧服务并阻塞（联调 / 解耦用）
sbx ui                   # 打开图形配置界面（本机浏览器，勾选即配置、一键启动）
```

`run` 的编排：密钥就绪检查 → 启动 broker / gateway / injector → 准备一次性工作区
→ `docker run` 容器 → 退出后 diff 审批回写。

### 6.10 docker — agent 镜像与多 agent 适配

- 基础镜像安装 Claude Code 与 Codex CLI，建非 root 用户 `agent(10001)`。
- `entrypoint.sh` 按 `SBX_AGENT` 分支，将每个 MCP server 注册为指向 broker 的**无密钥 URL**：
  - **Claude Code**：`claude mcp add --transport http <name> <url>`（用户作用域）。
  - **Codex**：`codex mcp add <name> --url <url>`，写入 `~/.codex/config.toml` 的 `[mcp_servers.<name>] url=...`
    （`url` 键即 Streamable HTTP）；并写一个自定义模型 provider，`base_url` 指向注入代理的 `/openai/v1`，
    `wire_api=responses`、`env_key=OPENAI_API_KEY`（容器内仅占位值），使模型 key 也不落沙箱。
- 两种 agent 的 MCP 都指向同一个 broker；凭据注入通过 injector 的 header 模板统一处理
  （`x-api-key: <密钥名>` 或 `Authorization: "Bearer ${密钥名}"`）。
- `dsh` 等其它 agent：CLI 入口已预留，接入具体目标后在此补齐镜像与注册逻辑。

---

## 7. 关键数据流

### 7.1 密钥生命周期（证明「不落沙箱」）

```
用户输入 ──sbx secret set──▶ 宿主机密钥库 ──broker 取出注入 env──▶ 真实 MCP server（容器/进程）
                                                                          │
容器内 agent ◀── JSON-RPC over HTTP（无密钥） ──── broker ◀── stdio ───────┘
```

密钥从未越过容器边界。

### 7.2 一次 MCP 工具调用

```
agent → POST http://host:8771/github {jsonrpc,id,method:tools/call}
      → broker 懒加载 github server（注入 token）→ 写 stdin
      → server 经白名单网关访问 api.github.com → 返回 stdout
      → broker 按 id 匹配 → application/json 返回 agent
```

### 7.3 一次模型调用

```
agent → POST http://host:8773/anthropic/v1/messages（带占位 key）
      → injector 剥前缀 + 覆盖写入真实 x-api-key → 转发 https://api.anthropic.com
      → 响应原样回传 agent
```

---

## 8. 安全模型

### 8.1 威胁与缓解

| 威胁 | 缓解 |
|---|---|
| 读取宿主机密钥文件（`~/.ssh`、`~/.aws`） | 只挂当前工作区，其余宿主机文件系统不可见 |
| 窃取 MCP 的 API key | 密钥只注入到 MCP server 侧，agent 容器拿不到 |
| 窃取自身的模型 key | 容器内只有占位 key，真实 key 由宿主机 injector 注入 |
| 被投毒的 MCP server 反噬宿主机 | MCP server 运行在受限容器中，而非宿主机进程 |
| 数据外泄 / C2 回连 | 出网 deny-by-default，仅白名单域名放行 |
| 容器逃逸 / 提权 | 非 root + `cap-drop ALL` + `no-new-privileges` + 只读 rootfs |
| 失控进程 / fork 炸弹 / 内存耗尽 | `--memory` / `--cpus` / `--pids-limit` 资源上限 |
| 提示注入触发破坏性操作 | 影响范围限制在一次性容器内；改动须经 diff 审批才回写 |
| 悄悄改坏项目文件 | 一次性工作区：改动先落副本，经审批才离开沙箱 |
| 事后无法追责 | 全量审计日志（egress / mcp / llm / secret） |

### 8.2 当前原型的已知局限

- 容器**共享宿主机内核**，隔离强度弱于虚拟机级隔离。
- 出网仅做**域名**白名单，未做 IP / SNI 级校验，也未签发临时受限凭据。
- injector 对容器侧为明文 HTTP（限于本机回环范围），真实 key 的 TLS 仅覆盖 injector→上游一段。
- broker 丢弃 server→client 通知，单会话模型。
- `host.docker.internal` 依赖对应的 Docker 运行时支持，部分 Linux 环境需额外配置。

---

## 9. 验证

| 验证点 | 方法 | 状态 |
|---|---|---|
| 凭据不落沙箱 | 容器内 `env` / MCP 配置无真实 key | 设计保证 + 单测 |
| 密钥注入 + 桥接 | mock stdio server 回带注入值 | ✅ 单测 |
| 模型 key 不落沙箱 | injector 覆盖占位值、剥前缀、转发 | ✅ 单测 |
| 审计日志 | JSONL 合法、判定正确、只记密钥名 | ✅ 单测 |
| 出网放行 / 拦截 | 实时代理：白名单内连通、外拦截 | ✅ 单测 |
| 白名单判定 | 精确 / 子域 / 伪装前缀用例 | ✅ 单测 |
| 容器加固 | 断言只读 / 资源上限 / 无密钥泄露 / 注入占位 key | ✅ 单测 |
| 一次性工作区 | 改/增/删后 Diff 检出、审批前原目录不变、Apply 正确回写 | ✅ 单测 |
| 配置解析 / 去重 | YAML 解析 + SecretNames 去重 | ✅ 单测 |

**端到端联调（真机）**：在 macOS + Docker 上跑通整条链路：
镜像非 root、只读 rootfs、能力全丢；broker 真实拉起 MCP server 容器并返回工具列表；
出网白名单实弹放行/拦截；agent 侧配置中只有 broker URL；容器看不到宿主机文件；审计日志完整记录。
其中 **Codex 全链路已用真实凭据验证**：容器内 Codex 仅持占位 key 发起 `codex exec`，
injector 在宿主机注入真实 key 后转发 OpenAI 兼容上游，模型正常返回，真实 key 全程不在容器内。

```bash
go test ./...   # 全绿
```

---

## 10. 演进路线

1. **可插拔隔离后端**：在不改动上层的前提下，从容器切换到虚拟机级隔离以进一步提升隔离强度。
2. **出网网关增强**：域名 + IP / SNI 级校验、出网流量上限、临时受限凭据签发。
3. **broker 增强**：多会话、server→client 通知回推（SSE）。
4. **更多凭据源**：对接 1Password / Vault 等外部密钥库；OAuth 流代持与自动刷新。
5. **细粒度策略**：按 MCP 工具粒度的 allow/deny，以及运行中的操作审批门。
6. **图形界面**：桌面 GUI 封装 + 信任级别预设，进一步降低使用门槛。

> 已落地能力：进程/文件/网络隔离与加固、MCP 凭据不落沙箱、模型 key 不落沙箱、
> MCP server 容器化、出网白名单、审计日志、一次性工作区 + diff 审批。
