# 项目总目标（Goal）

> 文档结构门禁回填（2026-09-22）：按 vibe-coding-workflow skill 的必需章节重组，并同步纠正已过期的"当前任务目标"。

## 项目背景
- **业务场景**：AI 编码工具（Cline / Roo / opencode / Claude Code 等）通常需要用户分别配置各家模型服务商的 Key、额度与协议；账号分散、额度易耗尽、429 限流无法自动切换。本项目提供统一的本地反向代理：对外暴露一套 OpenAI / Anthropic / Responses 兼容接口，对内汇聚多个 Cline 账号池与 opencode zen 免费模型，实现自动轮询、熔断换号、协议互转与统一管理。
- **目标用户**：使用 AI 编码工具的开发者（首要用户为本项目所有者本人），诉求是"统一入口接入、自动限流保护、统一观测"。
- **部署形态**：单二进制、零外部构建依赖（Go 编译产物直接运行），提供内嵌管理后台。

## 总目标
提供生产可用的 Cline API 反向代理服务，具备：
1. 多账号池：OAuth 自动刷新、轮询、429 冷却、失效摘除；
2. 协议互转：OpenAI Chat Completions / Anthropic Messages / OpenAI Responses 三态互通；
3. 上游聚合与容错：Cline 账号池 + opencode zen 免费模型 + 多渠道组轮询熔断（需求1）；
4. 能力分级路由：auto 组按 tier / context_window / vision / tool_call 自动选路（需求2）；
5. 运维能力：余额探活、自动签到、阈值告警（需求3）；
6. 治理能力：admin REST 全 API 化、append-only 审计、外挂巡检 agent（需求4）；
7. 可视化管理：内嵌 WebUI（不引入前端构建链）。

## 成功标准
- **功能成功标准**：四大需求（①渠道组轮询熔断 ②auto 能力分级路由 ③余额探活签到 ④外挂巡检 agent）端到端可用；WebUI 各 tab 面板可用；既有 OpenAI / Anthropic / Responses 三态转发零回归。
- **质量成功标准**：`go build` / `go vet` / `go test` / `go test -race` 全绿且在 CI 自动执行；单渠道失败不击穿整体服务；新增逻辑须覆盖主流程与至少一个失败分支。
- **交付成功标准**：变更可追溯（1 WP = 1 commit）、文档与事实一致、每个里程碑有可复现验证证据。

## 范围边界（做 / 不做）
**做**：
- 多账号池、协议互转、zen 接入、管理后台（既有能力维护）
- M0 安全与地基（admin 鉴权、生产级 server 骨架、凭据加密、Provider 接口、WebUI 骨架）
- M1 渠道组 + 轮询熔断（需求1）
- M2 能力分级 + auto 路由（需求2）
- M3 余额探活 + 签到（需求3）
- M4 外挂巡检 agent + 审计（需求4）
- 文档体系与 CI 门禁建设

**不做**：
- 上游 PR#11 的 ClinePass / Codex 新功能（feature 非 bug）
- 上游 PR#6 的 UI 全面换肤（主观审美改动）
- 上游 PR#4 的过时开发快照（已被 main 后续演进覆盖）
- 引入数据库（保持文件落盘）
- 引入前端构建链（React / Vue / npm）
- 重构协议转换逻辑、无谓新增依赖

## 约束条件
- **技术约束**：Go 1.25+（`go.mod` 声明 `go 1.25.0`；本地验证工具链 go1.26.6；CI 采用 `1.26.x`）；运行时依赖仅 `utls` + `golang.org/x/net`；单二进制 + `//go:embed`；路由使用 Go 1.22+ method-aware `ServeMux`（不引入 chi）；并发原语用 mutex+map / errgroup（不引第三方框架）；凭据禁止明文落盘（AES-GCM 加密）。
- **时间约束**：严格按 M0→M1→M2→M3→M4 顺序推进，不得跳级（M2 依赖 M1 熔断，M3 依赖 M0 加密，M4 依赖全部 API 化）；粒度 1 WP = 1 commit、1 M = 1 tag。
- **环境约束**：开发环境为 Android proot Ubuntu，`go test -race` 因 ThreadSanitizer 不支持其内存布局（`unsupported VMA range`）而不可用，故 -race 门禁必须由 CI（真内核）承担，不得以本地通过替代。

## 当前任务目标（滚动更新）
**2026-09-22（当前）**：修复文档体系与 CI 门禁缺口 —— 4 个核心文档按 skill 必需章节重组并同步事实；CI 补齐 `go vet` / `go test` / `go test -race` 门禁，并阻断"测试未过仍发布"的路径。

> 历史：最初任务目标为"移植上游 GitHub 未合并 PR 中的 4 个真实 bug 修复"，该任务已完成（commit 9485063 / 181f547 / 3167549）。

## M0-M4 修正案（2026-09-22 深度论证）
依据：go-backend skill（生产级 server 骨架 / 并发 / 错误处理 / 测试）、use-modern-go CLI（Go 1.25 48 条指南）、agent-governance skill（M4 治理模式）、代码实锤（proxy.go:296 零超时配置）。

- M0 增补：http.Server 必须补 ReadHeaderTimeout / IdleTimeout + signal.NotifyContext 优雅关闭（现状 proxy.go:296 零超时字段，Slowloris 暴露面实锤）；admin 鉴权 = agent-governance 的 Strict 级。
- M1 修正：路由**不引入 chi** —— 用 Go 1.22+ method-aware ServeMux + r.PathValue（use-modern-go: http_servemux_patterns），与现有 mux 风格零迁移成本。
- M1 并发基线：熔断器 = mutex+map（skill: 细粒度互斥用 mutex）；探活 fan-out = errgroup SetLimit（Go 1.22+ 无 loopvar 坑）；错误分派 = sentinel + errors.Is（ErrRateLimited{RetryAfter} / ErrKeyInvalid）。
- M3 修正：签到 / 同步调度器每个 goroutine 绑定 ctx 停止条件（skill: goroutine 无退出路径即泄漏）；多结果聚合用 errors.Join。
- M4 修正：巡检 agent 采用 agent-governance 模式 —— policy as YAML、allowlist admin API、写操作 require_human_approval、append-only 审计 JSONL、fail closed、Open/Standard/Strict/Locked 分级放权（从 Standard 起步）。
- 代码风格基线：新代码用 wg.Go(1.25) / atomic.Bool / OnceFunc / slices·maps 包 / min-max / any / json omitzero；存量不整片重写（skill: Apply to generated changes）。
- 测试基线：httptest.NewServer 假上游测熔断 / 轮询全链路（不 mock http.Client）；go test -race 必须进 CI。
