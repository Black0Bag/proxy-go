# 编码规范（Rules）

本次任务沉淀的规则（沿袭项目既有风格）：

1. **接口透传完整性**：http 中间件包装 ResponseWriter 时，必须透传 `http.Flusher`（及未来可能用到的 `http.Hijacker`）等能力接口，否则 handler 侧能力断言失败。新增包装层时同步补测试。
2. **类型断言安全**：对 `map[string]any` 取值后禁止直接 `.(string)` 裸断言用于日志/展示路径；必须 `, ok` 双返回或 `fmt.Sprintf("%v", ...)`。上游 JSON 结构不可信。
3. **流式降级原则**：SSE handler 必须在写任何 header 之前完成 Flusher 能力检测；不可用时走聚合降级而不是静默 return。
4. **移植外部修复**：只移植 bug 修复（fix 类），feature/审美改动不入修复分支；每个移植 commit message 标注来源 PR 编号。
5. **测试基线**：修复类改动至少附一条可复现验证（本次：statusWriter 单测 + go build/vet/test 三绿）。
