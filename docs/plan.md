# 实施计划（Plan）

> 文档结构门禁回填（2026-09-22）：按 vibe-coding-workflow skill 必需章节（里程碑 / 任务拆解 / 验收节点 / 风险与回滚）重组；历史计划内容完整保留于文末归档区，未删除任何既有信息。

## 里程碑
| 里程碑 | 内容 | 状态 |
|---|---|---|
| M0 | 安全与地基（admin 鉴权 / 生产级 server 骨架 / 凭据加密 / Provider 接口 / WebUI 骨架） | ✅ 完成 |
| M1 | 渠道组 + 轮询熔断（需求1） | ✅ 完成：库 + 非流式接线 + **流式 TTFB 接线**（`CLINE_PROXY_DISPATCH` 默认关闭） |
| M2 | 能力分级 + auto 路由（需求2） | ✅ 完成：规则引擎 + 能力标签 + 按序调度 + **渠道组转发通路**（`CLINE_PROXY_GROUP_ROUTING` 默认关闭） |
| M3 | 余额探活 + 签到（需求3） | ⚠️ 部分完成：5 家探针可用，调度器 / 阈值通知 / UI 待做 |
| M4 | 外挂巡检 agent（需求4） | ⚠️ 部分完成：admin REST + 审计 JSONL 就绪，agent v1/v2 待做 |
| M5 | **门禁与工程化（本轮）**：文档结构门禁 + CI 测试门禁 | 🔄 本轮执行 |
| M6 | WebUI 面板填充（各 M 的 UI 工作包） | ⏳ 待排 |

## 任务拆解
| 任务 | 目标 | 输入 | 输出 |
|---|---|---|---|
| WP5.1 文档结构门禁 | 4 个核心文档通过 `ensure_core_docs.py` 校验且内容与事实一致 | skill 必需章节定义、仓库实测结构 | 重写后的 `goal.md` / `plan.md` / `rules.md` / `structure.md` |
| WP5.2 CI 测试门禁 | CI 自动执行 `go vet` / `go test` / `go test -race`，且测试未过时阻断发布 | `.github/workflows/build.yml`、go.mod | 含 `test` job 的 CI 配置，`release` 依赖 `test` |
| ~~WP1.5-流式~~ ✅ 完成 | 流式请求也享受轮询熔断，且首字节后绝不可重试 | `dispatch.go` 的 `ErrTTFB` | `internal/app/ttfb.go`：可取消发射 + TTFB 首块边界 + `replayBody` 回放 |
| ~~WP2.3a 基础层~~ ✅ 完成 | 能力标签 + auto 候选计算 + 按序调度（粘性/熔断语义） | Channel 无 Caps；Balancer 只能按组策略选 | `Channel.Caps`、`AutoCandidates`、`DispatchOrdered` + 16 个单测 |
| ~~WP2.3b 转发通路~~ ✅ 完成 | `model=auto` 端到端可用 | 通用渠道转发实现 | `group_route.go`：OpenAI 兼容发射 + 组路由接线 + 账号门禁修正 + 16 个单测 + 主路径冒烟 |
| WP3.2-3.4 签到 / 阈值 / 面板 | 签到调度器、阈值动作通知、admin 面板 | 探针与渠道 metadata | 调度器 + 通知 + UI |
| WP4.3-4.4 巡检 agent | v1 定时巡检日报；v2 独立进程 + policy | admin REST API、审计 JSONL | 巡检 agent + policy YAML |

## 验收节点
- **验收点 1（WP5.1）**：`python3 scripts/ensure_core_docs.py --project-root . --check-only` 输出 4 个文档全部为 `[EXISTS]` 且**无 `[INVALID]`**、无 `[WARN]`。
- **验收点 2（WP5.2）**：CI 配置文件语法有效，存在 `test` job，且 `release` job 的 `needs` 包含 `test`；本地可复现 `go vet` / `go test` 全绿。
- **验收点 3（通用）**：每 WP 过 `go build` / `go vet` / `go test`；M 级另过冒烟（E2E）。
- **验收点 4（WP1.5-流式）** ✅ 已验证：`ttfb_test.go` 用 httptest 假上游覆盖 —— 上游返 200 响应头后
  挂起 → `ErrTTFB` 换渠道（0.21s 内放弃，不挂死）；正常流 → 首块完整回放（`body == chunk1+chunk2`，
  防「首字节丢失」类回归）；Dispatch 组合 → 挂起渠道自动换到健康渠道且客户端拿到完整流。
- **验收点 5（发布安全）**：任一测试失败时，`release` job 不执行、不发新 tag。

## 风险与回滚
- **通用回滚纪律**：所有改动以 commit 粒度落盘，`git revert <commit>` 即可完全回滚；1 WP = 1 commit，禁止把多个 WP 混进同一提交。
- **本轮风险 1（CI 变更影响发布流程）**：让 `release` 依赖 `test` 后，测试失败会阻断发版——这是**预期行为**，但若测试本身存在环境脆弱性（如依赖外网），可能造成误阻断。
  - 缓解：测试须可重复、不依赖外网与真实凭据；`-race` 仅在 ubuntu-latest 执行（真内核 + gcc 可用）。
  - 回滚：还原 `.github/workflows/build.yml` 至改动前版本（移除 `test` job、恢复 `release.needs: build`）。
- **本轮风险 2（-race 无法本地验证）**：本地 proot 环境 ThreadSanitizer 不可用（`unsupported VMA range`），无法在本地证明 `-race` 通过。
  - 缓解：如实标注该限制，首次 CI 运行需人工观察；`-race` 与普通 `go test` 均保留，避免单点失败导致门禁整体失效。
  - 回滚：无代码影响，属配置层，可直接调整。
- **本轮风险 3（文档重写引入失真）**：重写 `structure.md` 若引入未核实描述，会制造新的"文档与事实不符"。
  - 缓解：所有行数、文件清单均来自实测（`wc -l`）；历史内容不删除，仅重组。
- **历史风险（PR 移植期）**：cherry-pick 若因 main 演进产生冲突 → 人工比对 PR diff 逐块解决；降级聚合路径为纯新增分支，不影响正常流式路径。

---

## 本轮详细计划：WP5 文档结构门禁 + CI 测试门禁（2026-09-22）

### 背景与门禁证据
`ensure_core_docs.py --check-only` 实测结果：4 个文档均为 `[EXISTS]` 但**全部 `[INVALID]`**——
- `goal.md` 缺：`## 项目背景` / `## 总目标` / `## 成功标准` / `## 范围边界（做 / 不做）` / `## 约束条件`
- `plan.md` 缺：`## 里程碑` / `## 任务拆解` / `## 验收节点` / `## 风险与回滚`
- `rules.md` 缺：8 个必需章节全部缺失
- `structure.md` 缺：`## 模块划分` / `## 数据流/调用关系` / `## 依赖与外部接口` / `## 关键入口文件`

CI 门禁证据：`.github/workflows/build.yml`（115 行）仅有 `build`（多平台 matrix）与 `release`（语义化打 tag）两个 job，**无 `go vet`、无 `go test`、无 `-race`**；且 `release.needs: build`，即测试即使失败也照样发版。

### 改动范围
- `docs/goal.md`：补齐 5 章节 + 纠正过期的"当前任务目标"（原停留在 PR 移植期）。
- `docs/plan.md`：补齐 3 章节 + 更新进度表与遗留清单（不删除历史）。
- `docs/rules.md`：补齐 8 章节，沉淀可复用规则。
- `docs/structure.md`：按实测重写（原"与 b07b46e 一致、proxy.go 2042 行"与事实严重不符）。
- `.github/workflows/build.yml`：新增 `test` job（vet + test + -race），`release` 改为 `needs: [build, test]`。

### 验证方式
1. `python3 <skill>/scripts/ensure_core_docs.py --project-root /workspace/Cline-proxy --check-only` → 无 INVALID / 无 WARN。
2. `go build ./... && go vet ./... && go test ./...` 全绿。
3. CI YAML 语法校验（如 `actionlint` 或 python yaml 解析）。
4. `git diff --stat` 核对改动范围与计划一致。

### 明确不做（本轮）
- 不升级 Actions 大版本（实测 `setup-go` 最新 v7.0.0、`checkout` v7.0.1、`upload-artifact` v7.0.1，跨大版本升级需独立验证，避免与门禁变更混合）。
- 不整仓 `gofmt`（既有 10 个文件格式债，单独立项）。
- 不改任何业务逻辑（`internal/**` 非文档文件零改动）。

---

# 归档：历史计划与路线图（原文保留）

## 任务定性（PR 移植期）
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

## 实施步骤（PR 移植期）
1. `git cherry-pick 76a032b`（PR#5：proxy.go 降级聚合 + panic 修复；与 main 无分叉，应干净应用）
2. `git cherry-pick f832b02`（PR#10：logs.go Flush 增强 + admin_html.go toast；与步骤 1 无文件交集，无冲突）
3. 新增 `internal/app/logs_test.go`：statusWriter 实现 http.Flusher 接口断言 + Flush 透传与状态补写测试（项目首个测试）
4. 验证：`go build ./... && go vet ./... && go test ./...`（PATH 需含 /usr/local/go/bin）

## 影响范围（PR 移植期）
- internal/app/logs.go（+10 行）
- internal/app/proxy.go（约 +165/-10 行，流式 handler 两处）
- internal/app/admin_html.go（1 行）
- internal/app/logs_test.go（新增）
- docs/*.md（新增，不影响构建）

## 验证方式（PR 移植期）
1. 编译通过（go build）
2. vet 零告警
3. 新增单测通过（go test）
4. 冒烟：构建产物可启动并响应 /admin/（如环境允许）

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
| M1 渠道组+轮询熔断（WP1.1-1.5） | ⚠️ 库完成，流式未接入 | f8c5d93, fc2ebf0, 3ee95e7 |
| M2 auto 路由规则引擎（WP2.1-2.2 核心） | ✅ 完成 | 9eb56fd |
| M3 余额探针 5 家（WP3.1 核心） | ✅ 完成 | 9eb56fd |
| M4 admin REST + 审计（WP4.1-4.2） | ✅ 完成 | 84a7b9d |
| WebUI 骨架重构（WP0.5） | ✅ 完成 | 240d1d3 |
| WP1.5-接线（非流式 Dispatch 接入） | ✅ 完成 | ffdffcd |
| WP5 文档 + CI 门禁 | ✅ 完成 | 748989f, 424ff10 |
| WP1.5-流式（TTFB 首块边界接线） | ✅ 完成 | 3d92f18 |
| WP2.3a auto 基础层（能力标签 + 候选计算 + 按序调度） | ✅ 完成 | 5c6a51b |
| WP2.3b 渠道组转发通路（组路由接入主路径） | ✅ 完成 | 见本轮 commit |

**WP2.3b 主路径冒烟证据（真实二进制 + 假上游，3/3 符合预期）**：
1. `CLINE_PROXY_GROUP_ROUTING=1` + `model=smoke-grp` → 假上游返回 `{"ok":true,"from":"fake-upstream"}`，代理日志出现 `group "smoke-grp": dispatch ... strategy=auto members=1` 与 `upstream ok channel=c1`；
2. 开关关闭 + 同请求 → 保持原 401（"No accounts in pool"），行为与接线前一致（可回退）；
3. 开关开启 + `model=other-model`（非组名）→ 仍 401，证明账号门禁仅对"命中组名"的请求精确放行。

E2E 冒烟 8/8：401 鉴权/渠道创建/组创建/列表/审计 JSONL/配置落盘/探针 502/优雅退出。

## 遗留（下轮可继续）
- ~~WP1.5-流式接入~~ **已于本次完成**（详见执行进度表与验收点 4）。
- TTFB 超时与「推理型慢首 token」的平衡待实测：默认 15s，可用 `CLINE_PROXY_TTFB_TIMEOUT`
  （Go duration，如 `8s`；非法值回退 15s）调整。若某上游为长思考模型且首 token 稳定超过该阈值，
  会被判为挂起并换渠道，需按真实模型表现校准阈值。
- 开关关闭时的主路径仍不可取消：`callClineAPI` 走 `context.Background()`，客户端断开不会中止
  上游请求（与接线前行为一致，故未在本 WP 改动）；如需覆盖，应作为独立 WP 处理。
- ~~WP2.3b 通用渠道转发通路~~ **已完成**（见执行进度表与冒烟证据）。
- **渠道组用量统计未接入 stats**：组转发为纯字节透传，未解析 usage/token（渠道组成本统计缺位，
  如需要应单独立项做流式 usage 聚合，避免阻塞本 WP）。
- **组路由与 admin APIKey 门禁的关系**：组请求同样要求客户端携带 proxy API key
  （走 apiKeyHandler），仅放行"账号池为空"这一前置检查；客户端鉴权不变。
- **WP2.4**：admin 标签编辑界面（`Channel.Caps` 已可序列化，仅缺 UI 表单）。
- **安全债（新发现，非本轮引入）**：`data/channels.json` 中渠道 `APIKey` **明文落盘**
  （`channel.go` 的 `Save` 注释明示"含 APIKey"），与 goal.md 约束「凭据禁止明文落盘（AES-GCM）」
  不符；`secretbox.go` 目前只覆盖账号池 token。建议单独立项按 M0 加密方案处理，需提供迁移与回滚。
- WP3.2-3.4：签到调度器、阈值动作通知、admin 面板 UI
- WP4.3-4.4：agent v1 定时巡检（可用本机定时任务零代码实现）、agent v2 独立进程
- WebUI 渠道组/auto/余额 tab 面板填充（骨架已留 tab 位）
- Actions 组件大版本升级（setup-go v5→v7.0.0、checkout v4→v7.0.1、upload-artifact v4→v7.0.1）——需独立验证 breaking change
- `internal/app` 既有 10 个文件 `gofmt` 不规范——建议单独立一个纯格式化 commit
