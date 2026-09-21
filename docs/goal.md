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

## 完成定义
- 4 个 bug 全部修复且可追溯（commit 关联 PR 编号）
- `go build ./...`、`go vet ./...`、`go test ./...` 全部通过
- statusWriter.Flush 有单元测试覆盖（项目首个测试文件）
