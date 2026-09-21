# 项目目标（Goal）

## 总项目目标
Cline-proxy：Cline API 反向代理服务（Go 单二进制），支持多账号轮询、OpenAI/Anthropic/Responses 协议互转、opencode zen 免费模型接入、管理后台。

## 当前任务目标（2026-09-22）
移植上游 GitHub 未合并 PR 中的真实 bug 修复到本地 main（b07b46e），使 SSE 流式、稳定性、管理端提示恢复正确行为。

## 做与不做边界
**做：**
- Bug1：statusWriter 未实现 http.Flusher → 所有 SSE 流式响应被中间件吞掉（根因修复，取 PR#10 方案）
- Bug2：非流式聚合日志处 `getNested(...).(string)` 裸断言，content 非 string 时 panic（取 PR#5 方案）
- Bug3：Flusher 不可用时无降级路径，客户端拿不到任何响应体（取 PR#5 的聚合降级方案）
- Bug4：admin 模型同步 toast 读取 `d.data.message` 与接口实际返回结构不符（取 PR#10 方案）

**不做：**
- PR#6 的 UI 全面换肤（主观审美改动，非 bug）
- PR#11 的 ClinePass/Codex 新功能（feature 非 bug）
- PR#4 的过时开发快照（+858 行，已被 main 后续演进覆盖）
- 不重构、不改协议逻辑、不加新依赖

## Skill 深度论证后的 M0-M4 修正案（2026-09-22）
依据：go-backend skill（生产级 server 骨架/并发/错误处理/测试）、use-modern-go CLI（Go 1.25 48 条指南）、agent-governance skill（M4 治理模式）、代码实锤（proxy.go:296 零超时配置）。

- M0 增补：http.Server 必须补 ReadHeaderTimeout/IdleTimeout + signal.NotifyContext 优雅关闭（现状 proxy.go:296 零超时字段，Slowloris 暴露面实锤）；admin 鉴权 = agent-governance 的 Strict 级。
- M1 修正：路由**不引入 chi**——用 Go 1.22+ method-aware ServeMux + r.PathValue（use-modern-go: http_servemux_patterns），与现有 mux 风格零迁移成本。
- M1 并发基线：熔断器 = mutex+map（skill: 细粒度互斥用 mutex）；探活 fan-out = errgroup SetLimit（Go 1.22+ 无 loopvar 坑）；错误分派 = sentinel + errors.Is（ErrRateLimited{RetryAfter}/ErrKeyInvalid）。
- M3 修正：签到/同步调度器每个 goroutine 绑定 ctx 停止条件（skill: goroutine 无退出路径即泄漏）；多结果聚合用 errors.Join。
- M4 修正：巡检 agent 采用 agent-governance 模式——policy as YAML、allowlist admin API、写操作 require_human_approval、append-only 审计 JSONL、fail closed、Open/Standard/Strict/Locked 分级放权（从 Standard 起步）。
- 代码风格基线：新代码用 wg.Go(1.25)/atomic.Bool/OnceFunc/slices·maps 包/min-max/any/json omitzero；存量不整片重写（skill: Apply to generated changes）。
- 测试基线：httptest.NewServer 假上游测熔断/轮询全链路（不 mock http.Client）；go test -race 必须进 CI。
