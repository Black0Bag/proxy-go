# 项目架构（Structure）

> 事实核对（2026-09-22）：本文档已按仓库实际内容重写。历史版本（b07b46e 时期）声称"无结构性变化、proxy.go 2042 行"，与本轮实测严重不符——项目已新增 `dispatch.go` / `channel.go` / `balance_probe.go` / `auto_route.go` / `secretbox.go` / `adminauth.go` / `admin_channels.go` / `admin_zen.go` / `dispatch_wire.go` 及 9 个测试文件。
> 实测口径：全仓 Go 源码 **9865 行**，其中 `internal/app` **9100 行**。

## 目录结构
```
Cline-proxy/
├── main.go                      # 入口：CLI 参数解析、StartProxy 启动
├── go.mod                       # module cline-go-proxy；go 1.25.0
├── Dockerfile / docker-compose.yml
├── internal/
│   ├── app/                     # 主体业务包（9100 行）
│   │   ├── proxy.go             # 2184 行：HTTP 路由、三态模型分发、协议转换、流式转发
│   │   ├── admin.go             # 1072 行：管理后台 API
│   │   ├── zen.go               #  576 行：opencode zen 免费模型上游
│   │   ├── responses.go         #  550 行：OpenAI Responses API 适配
│   │   ├── compact.go           #  519 行：上下文压缩（opencode 摘要机制移植）
│   │   ├── pool.go              #  493 行：Cline 账号池：OAuth、轮询、429 冷却
│   │   ├── channel.go           #  420 行：Channel / ModelGroup 数据模型与持久化
│   │   ├── models.go            #  274 行：模型列表同步
│   │   ├── proxy_pool.go        #  268 行：出站代理池 + uTLS 指纹
│   │   ├── balance_probe.go     #  208 行：五家上游余额探针
│   │   ├── admin_channels.go    #  208 行：渠道 / 渠道组管理 API
│   │   ├── stats.go             #  199 行：token / 费用统计落盘
│   │   ├── admin_zen.go         #  193 行：zen 相关管理 API
│   │   ├── logs.go              #  186 行：请求日志中间件 + statusWriter
│   │   ├── dispatch.go          #  178 行：Balancer 调度（渠道挑选 + 熔断 + TTFB 边界）
│   │   ├── dispatch_wire.go     #  135 行：Dispatch 接入主转发路径（灰度开关）
│   │   ├── secretbox.go         #  133 行：AES-GCM 凭据加密
│   │   ├── auto_route.go        #  130 行：auto 路由规则引擎（RequestFeatures）
│   │   ├── adminauth.go         #   70 行：admin 鉴权中间件
│   │   ├── types.go             #   37 行：共享类型
│   │   ├── admin_html.go        #   24 行：内嵌入口（//go:embed 声明）
│   │   ├── admin/web/           # 内嵌前端资源：index.html / app.js / style.css
│   │   └── *_test.go            # 9 个测试文件，见"测试覆盖现状"
│   ├── cline/auth.go            # Cline WorkOS 认证
│   ├── kit/http.go              # HTTP 客户端工具（共享 HTTPClient）
│   └── provider/                # Provider 接口层
├── docs/                        # 核心文档（goal / plan / rules / structure）
└── .github/workflows/build.yml  # CI：多平台构建 + 语义化发布（+ 本轮新增测试门禁）
```

**测试覆盖现状（9 个测试文件）**：`secretbox_test.go`(220) / `balancer_test.go`(181) / `dispatch_wire_test.go`(132) / `dispatch_test.go`(121) / `breaker_test.go`(106) / `auto_route_test.go`(79) / `balance_probe_test.go`(73) / `adminauth_test.go`(73) / `logs_test.go`(58)。

## 模块划分
- **接入层 / 协议层**（`proxy.go`、`responses.go`）：对外暴露 OpenAI Chat Completions、Anthropic Messages、OpenAI Responses 三种协议入口，负责请求解析、协议互转与响应（含 SSE 流式）写回。
- **调度层**（`dispatch.go`、`dispatch_wire.go`、`channel.go`、`auto_route.go`）：渠道/渠道组模型、候选挑选策略（round_robin / weighted / least_used / lowest_latency）、熔断与半开探测、TTFB 边界、能力标签路由。
- **上游接入层**（`pool.go`、`zen.go`）：Cline 账号池（OAuth 刷新、轮询、429 冷却）与 opencode zen 免费模型上游；二者是"凭据与上游地址"的唯一事实来源。
- **传输层**（`proxy_pool.go`、`kit/http.go`）：出站 HTTP 客户端、代理池、uTLS 指纹伪装。
- **安全层**（`secretbox.go`、`adminauth.go`）：凭据加密存储与 admin 鉴权。
- **运维层**（`balance_probe.go`、`stats.go`）：余额探活与用量统计落盘。
- **管理后台**（`admin.go`、`admin_channels.go`、`admin_zen.go`、`admin_html.go`、`admin/web/`）：REST API + 内嵌单页 WebUI。
- **横切中间件**（`logs.go`）：请求日志、状态码记录、`http.Flusher` 能力透传。

## 数据流/调用关系
**主转发链路（chat/completions 类请求）**：
```
客户端请求
  → requestLogMiddleware（logs.go，包装 statusWriter，负责 Flusher 透传与状态记录）
  → 路由分发（proxy.go，method-aware ServeMux）
      ├─ 命中 zen 免费模型 → handleZenChat（zen.go）→ zen 上游
      └─ 命中 Cline 模型 → callClineAPI / callClineAPIViaDispatch
            ├─ 默认路径：callClineAPI → pickAccount（pool.go，账号池策略选号）→ callClineAPIOnAccount 发射
            └─ 灰度路径：callClineAPIViaDispatch（dispatch_wire.go，仅非流式且开关开启）
                  → syncAccountsToBalancer：把 active 账号同步为系统组 __cline_pool__
                  → Balancer.Dispatch（dispatch.go）：按策略选渠道 + 熔断判定
                  → attempt 闭包内 getAccountByID 反查账号 → callClineAPIOnAccount 发射
  → callClineAPIOnAccount：token 保障 → 401 刷新重试 → 429 冷却记账 → 上游 HTTP
  → 响应写回（流式：SSE + Flush；非流式：聚合）
```

**管理链路**：`/admin/*` → adminauth 中间件（无 token 401）→ admin*.go 各 REST handler → 渠道/组/zen 配置落盘。

**关键边界**：账号池（pool.go）与渠道组（channel.go）是两套模型，`dispatch_wire.go` 的 `syncAccountsToBalancer` 是二者唯一的同步桥；Channel 只存 `AccountID`，不落 token，凭据始终由账号池独占持有。

## 依赖与外部接口
- **Go 运行时依赖**：`github.com/refraction-networking/utls v1.8.2`（TLS 指纹）、`golang.org/x/net v0.57.0`；间接：`brotli`、`klauspost/compress`、`x/crypto`、`x/sys`、`x/text`。
- **上游接口**：Cline API（OAuth / chat completions）、opencode zen 免费模型端点、五家余额探针端点（DeepSeek / SiliconFlow / OpenRouter / Moonshot / newapi 系）。
- **对外接口**：`/v1/chat/completions`、Anthropic Messages、`/v1/responses`、`/v1/models`、`/admin/*` REST。
- **配置与落盘**：账号池明文文件（首次启动自动迁移为加密）、渠道/组 JSON、统计文件、审计 JSONL。
- **CI/发布接口**：`.github/workflows/build.yml`（多平台 matrix 构建 → 语义化打 tag → GitHub Release）。
- **环境开关**：`CLINE_PROXY_DISPATCH`（Dispatch 主路径灰度开关，默认关闭）。

## 关键入口文件
- `main.go`：进程入口，CLI 参数与 `StartProxy` 启动。
- `internal/app/proxy.go`：HTTP 路由注册与主转发 handler（含 `/v1/chat/completions`、`handleAnthropicMessages`、`handleZenChat`）。
- `internal/app/logs.go`：全局请求中间件，所有请求必经。
- `internal/app/pool.go`：账号池 `pickAccount` 与凭据获取，主链路的选号入口。
- `internal/app/dispatch_wire.go`：dispatch 灰度接线点（`dispatchEnabled` / `callClineAPIViaDispatch`）。
- `internal/app/admin_html.go` + `internal/app/admin/web/`：WebUI 入口与内嵌资源。
- `.github/workflows/build.yml`：CI 门禁与发布流程定义。

## 高风险模块
- **`proxy.go`（2184 行）**：协议转换 + 流式转发核心，同时承担路由与主链路。改动必须限定在明确分支内，不触碰协议转换逻辑；任何"顺手重构"都可能造成三态协议回归。
- **`logs.go`（中间件）**：所有请求必经，`statusWriter` 若未透传 `http.Flusher`，会导致全部 SSE 流式响应被吞（历史 Bug1）。新增包装层必须同步补能力透传测试。
- **`dispatch_wire.go` + `dispatch.go`（调度与灰度）**：直接决定"请求发给哪个账号"，错误会导致单渠道击穿或凭据串号。默认关闭 + 可开关回退是当前唯一安全阀。
- **`pool.go` + `secretbox.go`（凭据）**：涉及 OAuth token 的加密、迁移与刷新；加密迁移一旦出错影响全部账号可用性，需要回滚通道。
- **`adminauth.go`（鉴权）**：鉴权缺口等同于把账号池暴露给任意本地进程；必须 fail closed。
- **`.github/workflows/build.yml`（发布流程）**：改动会影响发版路径（如让 release 依赖 test 后，测试失败将阻断发版），需要明确回滚方案。

## 变更记录
- 2026-09-22：按实测重写全文（补"模块划分 / 数据流·调用关系 / 依赖与外部接口 / 关键入口文件"章节，纠正行数与文件清单）。
- 2026-09-22（前一版）：记录 PR 移植期调用链，对应 commit b07b46e。
