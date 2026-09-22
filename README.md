# proxy-go

Cline API 反向代理（Go）。单二进制、零外部构建依赖，对外暴露统一的 OpenAI 兼容接口，对内汇聚 Cline 账号池、opencode zen 免费模型与自定义渠道组：自动轮询、429 冷却、熔断换渠道、协议互转，并内置中文管理后台。

## 功能

### 网关与协议
- **三协议兼容**：`/v1/chat/completions`（OpenAI）、`/v1/messages`（Anthropic Messages）、`/v1/responses`（OpenAI Responses，Cursor 等客户端直连）
- **双上游统一网关**：按 `model` 自动路由 —— Cline 账号池与 opencode zen 免费模型
- **zen 模型动态同步**：自动拉取 zen 模型列表，新免费模型自动接入；付费模型显式拒绝
- **官方摘要压缩（opencode 机制移植）**：上下文超限时按官方算法生成锚定摘要并重组会话，失败自动降级截断
- **多 IP 轮询出口**：zen 上游支持 http/https/socks5 代理池，round_robin / random / fill 策略

### 可靠性与调度
- **账号池**：OAuth 自动刷新、多账号轮询（round_robin / fill / random）、429 冷却解析（"Try again in 17h 59m"）与到期自动恢复
- **渠道组 + 轮询熔断**：自定义多渠道组，失败自动换渠道；连续失败熔断、指数退避、半开探测恢复（灰度开关，见下）
- **能力路由（auto 组）**：按请求特征（tools / 图片 / 上下文长度）过滤渠道、按质量分级（S/A/B/C）排序调度，会话粘性锁定（灰度开关，见下）
- **流式保护**：首字节（TTFB）边界内允许换渠道，首块到达后锁定不再重试，客户端不丢流头部、不收重复输出
- **生产级 server 骨架**：读超时（防 Slowloris）、空闲连接回收、SIGTERM 优雅关闭

### 管理与运维
- **中文管理后台**：`/admin/` 管理账号、API Key、渠道与渠道组、zen 配置、代理池、请求统计
- **余额探活**：DeepSeek / SiliconFlow / OpenRouter / Moonshot / newapi 系上游余额查询
- **API Key 鉴权**：多 Key 生成/删除，保护代理端点
- **凭据加密**：账号池 token AES-GCM 加密落盘，首次启动自动从明文迁移
- **请求日志与统计**：JSONL 落盘，今日/累计聚合、按模型分布
- **System Prompt 覆盖**：项目目录放 `override.md` 自动替换系统提示词
- **账号导入**：OAuth 浏览器登录、手动 Token、批量文件导入

## 快速开始

```bash
# 构建（Go 1.25+）
go build -o cline-proxy .

# 启动（默认端口 3457，-host/-port 可调）
./cline-proxy

# 管理后台
# 浏览器打开 http://127.0.0.1:3457/admin/
```

Docker：

```bash
docker compose up -d
```

## 灰度开关

调度能力通过环境变量启用，默认关闭（关闭时代码路径与历史行为一致，可随时回退）：

| 环境变量 | 作用 | 默认 |
|---|---|---|
| `CLINE_PROXY_DISPATCH` | 主路径接入渠道调度（轮询熔断、失败换渠道） | 关闭 |
| `CLINE_PROXY_GROUP_ROUTING` | 启用渠道组路由（`model=组名` 走自配渠道组，`auto` 组走能力路由） | 关闭 |
| `CLINE_PROXY_TTFB_TIMEOUT` | 流式首字节等待上限（Go duration，如 `8s`） | `15s` |

示例：

```bash
CLINE_PROXY_DISPATCH=1 CLINE_PROXY_GROUP_ROUTING=1 ./cline-proxy
```

## 文档

- `docs/goal.md` —— 项目目标与范围
- `docs/plan.md` —— 路线图与执行进度
- `docs/rules.md` —— 编码规范
- `docs/structure.md` —— 架构与模块划分

## 构建

本地构建需要 Go 1.25+。CI（GitHub Actions）在 push 时自动执行 `go vet`、`go test`（含 `-race`），并按 tag 构建多平台二进制（linux / windows / darwin）。

---

## 致谢

本项目基于 [LINUX DO](https://linux.do) 社区分享的 Cline Go Proxy 项目二次开发，感谢原项目作者与社区。
