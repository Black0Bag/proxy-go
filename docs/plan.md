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


---

# 完整路线图 v1.0（2026-09-22 深度论证定稿）

前置事实：4 个上游 bug 已修复（9485063/181f547/3167549）；Go 1.26.6 本地可用；modern-go CLI 已装通（GOPROXY=goproxy.cn）；5 家余额端点已验证；OpenRouter auto 机制已核实。

## M0 安全与地基 —— 4 个工作包
- WP0.1 admin 鉴权：Bearer token + 可选登录页；agent-governance Strict 级；无 token 一律 401
- WP0.2 生产级 server 骨架：ReadHeaderTimeout=10s（Slowloris，实锤缺失于 proxy.go:296）、IdleTimeout=120s、signal.NotifyContext 优雅关闭（排空≤30s）
- WP0.3 凭据加密：AES-GCM 加密 token，主密钥环境变量/首启生成；旧 .cline-accounts.json 自动迁移
- WP0.4 Provider 接口：`ListModels/Chat/ProbeBalance/CheckIn`（CheckIn、ProbeBalance 可选实现）；Cline 池与 zen 首批接入；method-aware ServeMux 注册路由（不引 chi）
- 验收：build/vet/test/-race 全绿；admin 无 token 401；SIGTERM 排空；单测覆盖鉴权中间件

## M1 渠道组 + 轮询熔断（需求1）—— 6 个工作包
- WP1.1 数据模型：Channel{provider,key,模型映射,weight,state} / ModelGroup{对外名,成员[],策略}，JSON 落盘
- WP1.2 轮询器：round_robin / weighted / least_used / lowest_latency
- WP1.3 熔断器：allowed_fails=3、cooldown=5s 起步指数退避、半开探测；mutex+map 实现
- WP1.4 错误分类：sentinel 错误 ErrRateLimited{RetryAfter} / ErrKeyInvalid + errors.Is 分派；429/5xx/超时→熔断换渠道；401/403→dead；400→不换渠道
- WP1.5 流式保护：TTFB 超时 15s，仅首字节前允许 fallback；Retry-After 解析泛化（复用 pool.go "Try again in 17h"逻辑）
- WP1.6 测试：httptest.NewServer 假上游全链路（429→冷却→恢复→熔断→半开→恢复），go test -race 进 CI
- 验收：单渠道击穿不影响服务；轮询选择可观测（stats 落盘）

## M2 能力分级 + auto 路由（需求2）—— 4 个工作包
- WP2.1 标签 schema：tier(S/A/B/C)、context_window、vision、tool_call、reasoning、cost
- WP2.2 规则引擎：有 tools→tool_call=true；有 image→vision=true；ctx>32k→过滤 context_window；短小请求→省钱档（可选开关）
- WP2.3 auto 组：过滤后按 tier 降序进 M1 轮询；会话粘性（hash 锁定 60min）
- WP2.4 admin 标签编辑界面；LLM 分类器路由留接口不做实现
- 验收：curl /v1/chat/completions model=auto 端到端；粘性命中验证

## M3 余额探活 + 签到（需求3）—— 4 个工作包
- WP3.1 BalanceProbe：DeepSeek/SiliconFlow/OpenRouter/Moonshot/newapi系 五家端点已验证，probe 函数 + 渠道 metadata 配置驱动扩展
- WP3.2 签到调度器：每渠道 checkin{url,headers,successPattern}；全天随机窗口执行（反整点特征）；goroutine 绑 ctx 可停；多结果 errors.Join 聚合；CF 盾站点明确不支持并提示
- WP3.3 阈值动作：余额<阈值→自动降权/摘除 + webhook/TG 通知
- WP3.4 admin 展示余额与签到状态
- 验收：真实 DeepSeek key 走通 probe→展示→阈值动作全链路

## M4 外挂巡检 agent（需求4）—— 4 个工作包
- WP4.1 admin REST API 补全：渠道 CRUD/权重/启停全 API 化，全部过鉴权
- WP4.2 审计日志：append-only JSONL（谁/何时/改了什么/依据）
- WP4.3 agent v1（零代码）：本机定时任务 + subagent 调 admin API 巡检（错误率/成本/延迟/余额）→ 生成日报
- WP4.4 agent v2（可选）：独立进程，policy 写 YAML、API allowlist、写操作 require_human_approval、fail closed；放权从 Standard（只读+建议）起步，高风险动作永远人工
- 验收：连续 3 天巡检日报无漏报；写操作默认拦截待确认

## 执行纪律
- 粒度：1 WP = 1 commit；1 M = 1 tag（v0.x）
- 门禁：每 WP 过 build/vet/test/-race；M 级另过冒烟
- 回滚：git revert 对应 commit；M0 加密迁移提供 --rollback 恢复明文（过渡期）
- 顺序强制：M0→M1→M2→M3→M4，不得跳级（M2 依赖 M1 熔断，M3 依赖 M0 Provider/加密，M4 依赖全部 API 化）
- 范围外（明确不做）：PR#11 ClinePass/Codex feature、PR#6 UI 换肤、数据库引入、前端框架化


---

# WebUI 重构决策（2026-09-22，用户已授权全项目执行）

## 选型结论
- **不引入前端构建链**（React/Vue/npm）：保持"单二进制、零外部构建依赖"的项目哲学
- **方案**：admin_html.go 单文件内嵌 → `internal/app/admin/web/` 多文件 + `//go:embed`（Go 官方标准特性）
  - index.html（布局外壳 + tab 框架）
  - app.js（API 客户端封装 + 各面板逻辑，渐进迁移）
  - style.css（样式）
- **鉴权适配**：Bearer token；首次访问 URL 带 ?token= 或前端弹输入框存 localStorage；fetch 统一带 Authorization；401 时全局拦截弹框
- **迁移策略**：现有 1176 行功能零损失——style/script/body 三段机械拆分，现有功能归入"账号"tab，新功能（渠道组/auto/余额签到/巡检）各占新 tab 渐进实现

## 新增工作包
- WP0.5（M0）：WebUI 骨架重构（拆分 + embed + tab 框架 + API 客户端 + 鉴权接入）
- 各 M 的 UI 工作包在新骨架对应 tab 内扩展（WP1.7 渠道组面板 / WP2.4 auto 标签面板 / WP3.4 余额签到面板 / WP4.5 巡检面板）


---

# 执行进度（2026-09-22 一口气执行结果）

| 里程碑 | 状态 | commit |
|---|---|---|
| M0 安全与地基（WP0.1-0.5） | ✅ 完成 | 240d1d3, e29daf4 |
| M1 渠道组+轮询熔断（WP1.1-1.5） | ✅ 完成 | f8c5d93, fc2ebf0, 3ee95e7 |
| M2 auto 路由规则引擎（WP2.1-2.2 核心） | ✅ 完成 | 9eb56fd |
| M3 余额探针 5 家（WP3.1 核心） | ✅ 完成 | 9eb56fd |
| M4 admin REST + 审计（WP4.1-4.2） | ✅ 完成 | 84a7b9d |
| WebUI 骨架重构（WP0.5） | ✅ 完成 | 240d1d3 |

E2E 冒烟 8/8：401 鉴权/渠道创建/组创建/列表/审计 JSONL/配置落盘/探针 502/优雅退出。

## 遗留（下轮可继续）
- WP1.5-接线：Dispatch 接入 proxy.go 主转发路径（当前为库+测试完备，未切主路径，避免大爆炸）；已完成前置解耦：抽出 `callClineAPIOnAccount`，账号选择与请求发射分离（行为等价，主路径未切换）
- WP2.3：auto 组会话粘性入口在 Balancer 已有，主路径接线同上
- WP3.2-3.4：签到调度器、阈值动作通知、admin 面板 UI
- WP4.3-4.4：agent v1 定时巡检（可用本机定时任务零代码实现）、agent v2 独立进程
- WebUI 渠道组/auto/余额 tab 面板填充（骨架已留 tab 位）
- go test -race 移交 CI（proot TSan 限制）
