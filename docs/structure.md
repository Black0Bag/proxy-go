# 项目架构（Structure）

## 目录结构（与 b07b46e 一致，本次无结构性变化）
```
Cline-proxy/
├── main.go                  # 入口：CLI 参数、StartProxy 启动
├── internal/
│   ├── app/
│   │   ├── proxy.go         # 核心：HTTP 路由、三态模型分发、协议转换、流式转发（2042 行）
│   │   ├── pool.go          # Cline 账号池：OAuth、轮询、429 冷却
│   │   ├── zen.go           # opencode zen 免费模型上游
│   │   ├── compact.go       # 上下文压缩（opencode 摘要机制移植）
│   │   ├── proxy_pool.go    # 出站代理池 + uTLS 指纹
│   │   ├── logs.go          # 请求日志中间件、statusWriter、访问日志 API ← 本次修改
│   │   ├── admin*.go        # 管理后台 API + 内嵌单页 HTML ← 本次修改 admin_html.go
│   │   ├── stats.go         # token/费用统计落盘
│   │   ├── models.go        # 模型列表同步
│   │   ├── responses.go     # OpenAI Responses API 适配
│   │   └── logs_test.go     # 新增：statusWriter 测试
│   ├── cline/auth.go        # Cline WorkOS 认证
│   └── kit/http.go          # HTTP 客户端工具
├── docs/                    # 新增：skill 核心文档（goal/plan/rules/structure）
└── .github/workflows/       # 多平台构建
```

## 本次涉及的调用链
```
请求 → requestLogMiddleware(logs.go:130, 包装 statusWriter)
     → handleXXX / handleAnthropicStreamWithUsage(proxy.go)
     → w.(http.Flusher) 断言 [Bug1 修复点：statusWriter 补 Flush() 透传]
     → 非流式聚合日志 len(....(string)) [Bug2 修复点：安全断言]
     → Flusher 不可用分支 [Bug3 修复点：collectStreamResponse 聚合降级]
admin 页面 → POST /models/refresh → toast 读取 [Bug4 修复点：字段兼容]
```

## 高风险模块
- proxy.go：协议转换+流式转发核心，改动仅限 Flusher 检测顺序与降级分支，不触碰转换逻辑
- logs.go：所有请求必经的中间件，Flush 为纯新增方法，无行为回归面
