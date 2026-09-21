# 实施计划（Plan）

## 任务定性
- 场景：2（基于已有代码）+ 3（缺陷修复）
- 门禁等级：L2（多文件、需读调用链、移植外部修复）
- 用户授权：用户已明确指示"对本地代码一并解决"，本计划落盘即视为确认。

## 上游 PR 审计结论（侦察证据）
| PR | 状态 | 判定 |
|---|---|---|
| #1 | 已合并（ahead=0） | 无需处理 |
| #4 | 过时快照（behind_main=6，+858 行杂项） | 不移植 |
| #5 | 未合并，fix: SSE stream fallback | **移植**（Bug2+Bug3） |
| #6 | 未合并，UI 换肤 | 不移植（非 bug） |
| #9 | 未合并，Flush 透传简版 | 被 #10 覆盖，不单独移植 |
| #10 | 未合并，Flush 增强版+toast | **移植**（Bug1+Bug4） |
| #11 | 未合并，ClinePass/Codex feature | 不移植（feature） |

main 现状实锤：logs.go:109 statusWriter 仅有 WriteHeader/Write，无 Flush；proxy.go:266 存在裸断言；admin_html.go:1016 toast 读取字段错误。

## 实施步骤
1. `git cherry-pick 76a032b`（PR#5：proxy.go 降级聚合 + panic 修复；与 main 无分叉，应干净应用）
2. `git cherry-pick f832b02`（PR#10：logs.go Flush 增强 + admin_html.go toast；与步骤 1 无文件交集，无冲突）
3. 新增 `internal/app/logs_test.go`：statusWriter 实现 http.Flusher 接口断言 + Flush 透传与状态补写测试（项目首个测试）
4. 验证：`go build ./... && go vet ./... && go test ./...`（PATH 需含 /usr/local/go/bin）

## 影响范围
- internal/app/logs.go（+10 行）
- internal/app/proxy.go（约 +165/-10 行，流式 handler 两处）
- internal/app/admin_html.go（1 行）
- internal/app/logs_test.go（新增）
- docs/*.md（新增，不影响构建）

## 验证方式
1. 编译通过（go build）
2. vet 零告警
3. 新增单测通过（go test）
4. 冒烟：构建产物可启动并响应 /admin/（如环境允许）

## 风险与回滚
- 风险：cherry-pick 若因 main 演进产生冲突 → 人工比对 PR diff 逐块解决；降级聚合路径为纯新增分支，不影响正常流式路径
- 回滚：所有改动以 commit 粒度落盘，`git revert` 两个 cherry-pick commit 即可完全回滚；测试文件独立，直接删除即回滚
