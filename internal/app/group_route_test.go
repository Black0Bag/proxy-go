package app

// M2 WP2.3b 单测：渠道组转发通路。
//
// 覆盖：
//   - 开关解析（默认关闭）、组名匹配、会话指纹
//   - 上游发射：路径/鉴权/模型映射/stream 标记
//   - 主处理器：非流式透传、流式 SSE 透传、auto 组按序换渠道、组不存在、渠道全挂
//   - 失败状态码映射（401/403 不改写客户端鉴权语义）

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEnvTruthy 开关解析：默认关闭，仅显式真值开启。
func TestEnvTruthy(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false}, {"0", false}, {"false", false}, {"off", false},
		{"no", false}, {"unexpected", false},
		{"1", true}, {"true", true}, {"TRUE", true}, {" on ", true}, {"yes", true},
	}
	for _, c := range cases {
		t.Setenv("CLINE_PROXY_GROUP_ROUTING", c.val)
		if got := groupRoutingEnabled(); got != c.want {
			t.Errorf("val=%q: got=%v, 期望 %v", c.val, got, c.want)
		}
	}
}

// TestMatchGroupName 组名匹配（组名即对外模型名）。
func TestMatchGroupName(t *testing.T) {
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "c1"})
	b.UpsertGroup(ModelGroup{Name: "auto", Members: []string{"c1"}, Strategy: "auto"})
	b.UpsertGroup(ModelGroup{Name: "empty-group"})

	if name, ok := matchGroupName(b, "auto"); !ok || name != "auto" {
		t.Errorf("auto: ok=%v name=%q, 期望命中", ok, name)
	}
	if name, ok := matchGroupName(b, "  auto  "); !ok || name != "auto" {
		t.Errorf("带空白: ok=%v name=%q, 期望命中（应 TrimSpace）", ok, name)
	}
	if _, ok := matchGroupName(b, "empty-group"); ok {
		t.Errorf("空成员组不应命中")
	}
	if _, ok := matchGroupName(b, "nope"); ok {
		t.Errorf("未配置的名称不应命中")
	}
	if _, ok := matchGroupName(b, ""); ok {
		t.Errorf("空字符串不应命中")
	}
	if _, ok := matchGroupName(nil, "auto"); ok {
		t.Errorf("nil balancer 不应 panic 且不命中")
	}
}

// TestGroupSessionKey 会话指纹：同前缀稳定、异前缀不同、无消息为空。
func TestGroupSessionKey(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"messages": []any{
			map[string]any{"role": "system", "content": "you are a coding assistant"},
			map[string]any{"role": "user", "content": "fix the bug"},
		}}
	}
	k1 := groupSessionKey(base())
	if k1 == "" {
		t.Fatal("有消息时应产生指纹")
	}
	if groupSessionKey(base()) != k1 {
		t.Errorf("相同前缀应得到相同指纹")
	}
	// 后续消息不同不影响指纹（只取前两条）
	more := base()
	more["messages"] = append(more["messages"].([]any), map[string]any{"role": "assistant", "content": "done"})
	if groupSessionKey(more) != k1 {
		t.Errorf("后续消息不应改变指纹（前缀语义）")
	}
	// 前缀不同 → 指纹不同
	other := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "you are a coding assistant"},
		map[string]any{"role": "user", "content": "another task"},
	}}
	if groupSessionKey(other) == k1 {
		t.Errorf("不同前缀不应同指纹")
	}
	if groupSessionKey(map[string]any{}) != "" {
		t.Errorf("无 messages 应为空指纹")
	}
}

// TestGroupMessages 类型转换：跳过非法项，不 panic。
func TestGroupMessages(t *testing.T) {
	params := map[string]any{"messages": []any{
		map[string]any{"role": "user"},
		"invalid",
		map[string]any{"role": "assistant"},
	}}
	got := groupMessages(params)
	if len(got) != 2 {
		t.Fatalf("len=%d, 期望 2（跳过非法项）", len(got))
	}
	if groupMessages(map[string]any{}) != nil {
		t.Errorf("缺失 messages 应返回 nil")
	}
}

// TestEmitChannelRequest 发射语义：路径、鉴权、请求体（含 stream 标记）。
func TestEmitChannelRequest(t *testing.T) {
	var gotPath, gotAuth, gotCT string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	ch := &Channel{ID: "c1", BaseURL: srv.URL, APIKey: "k-test"}
	resp, err := emitChannelRequest(t.Context(), ch, map[string]any{"model": "test-model"}, false)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	defer resp.Body.Close()

	if gotPath != "/chat/completions" {
		t.Errorf("path=%q, 期望 /chat/completions", gotPath)
	}
	if gotAuth != "Bearer k-test" {
		t.Errorf("auth=%q, 期望 Bearer k-test", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type=%q", gotCT)
	}
	if gotBody["model"] != "test-model" {
		t.Errorf("model=%v, 无映射时应原样透传", gotBody["model"])
	}
	if gotBody["stream"] != false {
		t.Errorf("stream=%v, 期望 false", gotBody["stream"])
	}
}

// TestEmitChannelRequestModelMap ModelMap 生效：对外名 → 上游名。
func TestEmitChannelRequestModelMap(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch := &Channel{ID: "c1", BaseURL: srv.URL, ModelMap: map[string]string{"public-a": "upstream-a"}}
	resp, err := emitChannelRequest(t.Context(), ch, map[string]any{"model": "public-a"}, true)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	defer resp.Body.Close()

	if gotBody["model"] != "upstream-a" {
		t.Errorf("model=%v, 期望映射为 upstream-a", gotBody["model"])
	}
	if gotBody["stream"] != true {
		t.Errorf("stream=%v, 期望 true", gotBody["stream"])
	}
}

// TestEmitChannelRequestModelMapUnchanged 无映射的模型名不被改写。
func TestEmitChannelRequestModelMapUnchanged(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch := &Channel{ID: "c1", BaseURL: srv.URL, ModelMap: map[string]string{"public-a": "upstream-a"}}
	resp, err := emitChannelRequest(t.Context(), ch, map[string]any{"model": "other-model"}, false)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	defer resp.Body.Close()
	if gotBody["model"] != "other-model" {
		t.Errorf("model=%v, 期望不变", gotBody["model"])
	}
}

// TestEmitChannelRequestErrors 传入非法参数：base_url 为空应报错；调用方 params 不被修改。
func TestEmitChannelRequestErrors(t *testing.T) {
	if _, err := emitChannelRequest(t.Context(), &Channel{ID: "c1"}, map[string]any{}, false); err == nil {
		t.Errorf("空 base_url 应返回错误")
	}

	params := map[string]any{"model": "m"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	resp, err := emitChannelRequest(t.Context(), &Channel{ID: "c1", BaseURL: srv.URL}, params, true)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	defer resp.Body.Close()
	if _, exists := params["stream"]; exists {
		t.Errorf("不得修改调用方传入的 params（发现 stream 被写入）")
	}
}

// TestHandleGroupChatNonStreamPassthrough 非流式：状态码/正文/模型映射端到端。
func TestHandleGroupChatNonStreamPassthrough(t *testing.T) {
	const upstreamBody = `{"id":"x","choices":[{"message":{"role":"assistant","content":"hi"}}]}`
	var gotModel any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body["model"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer srv.Close()

	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "c1", BaseURL: srv.URL, ModelMap: map[string]string{"grp-model": "up-model"}})
	b.UpsertGroup(ModelGroup{Name: "grp-model", Members: []string{"c1"}, Strategy: "round_robin"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	handleGroupChatWith(rec, req, b, map[string]any{"model": "grp-model"}, "grp-model", false)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, 期望 200; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != upstreamBody {
		t.Errorf("body=%q, 期望原样透传 %q", rec.Body.String(), upstreamBody)
	}
	if gotModel != "up-model" {
		t.Errorf("上游收到 model=%v, 期望 up-model（ModelMap 生效）", gotModel)
	}
}

// TestHandleGroupChatStreamPassthrough 流式：SSE 字节完整透传且 Content-Type 正确。
func TestHandleGroupChatStreamPassthrough(t *testing.T) {
	const sse1, sse2 = "data: {\"delta\":\"a\"}\n\n", "data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(sse1))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(sse2))
	}))
	defer srv.Close()

	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "c1", BaseURL: srv.URL})
	b.UpsertGroup(ModelGroup{Name: "grp", Members: []string{"c1"}, Strategy: "round_robin"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	handleGroupChatWith(rec, req, b, map[string]any{"model": "grp", "stream": true}, "grp", true)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, 期望 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type=%q, 期望 text/event-stream", ct)
	}
	if rec.Body.String() != sse1+sse2 {
		t.Errorf("body=%q, 期望完整 SSE %q（首块回放 + 透传）", rec.Body.String(), sse1+sse2)
	}
}

// TestHandleGroupChatAutoFailover auto 组按序换渠道：
// tier=S 的首渠道 500 → 自动换到 tier=B 的健康渠道，客户端拿到完整响应。
func TestHandleGroupChatAutoFailover(t *testing.T) {
	var badCalls, goodCalls int
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badCalls++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer bad.Close()
	const goodBody = `{"ok":"recovered"}`
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(goodBody))
	}))
	defer good.Close()

	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "bad", BaseURL: bad.URL, Caps: &ModelCaps{Tier: "S"}})
	b.UpsertChannel(Channel{ID: "good", BaseURL: good.URL, Caps: &ModelCaps{Tier: "B"}})
	b.UpsertGroup(ModelGroup{Name: "auto", Members: []string{"bad", "good"}, Strategy: "auto"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	handleGroupChatWith(rec, req, b, map[string]any{"model": "auto"}, "auto", false)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, 期望 200（换渠道后成功）; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != goodBody {
		t.Errorf("body=%q, 期望来自健康渠道的 %q", rec.Body.String(), goodBody)
	}
	if badCalls == 0 {
		t.Errorf("tier=S 的首渠道应被优先尝试（AutoCandidates 降序）")
	}
	if goodCalls != 1 {
		t.Errorf("good calls=%d, 期望 1", goodCalls)
	}
}

// TestHandleGroupChatInvalidGroup 组不存在：502 + 明确错误信息。
func TestHandleGroupChatInvalidGroup(t *testing.T) {
	b := NewBalancer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	handleGroupChatWith(rec, req, b, map[string]any{"model": "ghost"}, "ghost", false)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code=%d, 期望 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not configured") {
		t.Errorf("body=%q, 应包含 not configured", rec.Body.String())
	}
}

// TestHandleGroupChatUpstreamDown 渠道全部不可达：返回 502/503 错误体，不 panic。
func TestHandleGroupChatUpstreamDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := srv.URL
	srv.Close() // 关闭后端口不可达

	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "dead", BaseURL: deadURL})
	b.UpsertGroup(ModelGroup{Name: "grp", Members: []string{"dead"}, Strategy: "round_robin"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	handleGroupChatWith(rec, req, b, map[string]any{"model": "grp"}, "grp", false)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code=%d, 期望 502", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("错误体应为 JSON: %v; body=%s", err, rec.Body.String())
	}
	if _, ok := got["error"]; !ok {
		t.Errorf("body=%q, 应包含 error 字段", rec.Body.String())
	}
}

// TestHandleGroupChatAutoNoCandidate auto 组无候选满足能力：503。
func TestHandleGroupChatAutoNoCandidate(t *testing.T) {
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "no-tool", BaseURL: "http://127.0.0.1:1", Caps: &ModelCaps{ToolCall: false}})
	b.UpsertGroup(ModelGroup{Name: "auto", Members: []string{"no-tool"}, Strategy: "auto"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	params := map[string]any{
		"model": "auto",
		"tools": []any{map[string]any{"type": "function"}},
	}
	handleGroupChatWith(rec, req, b, params, "auto", false)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d, 期望 503; body=%s", rec.Code, rec.Body.String())
	}
}

// TestGroupErrorStatus 失败状态码映射策略。
func TestGroupErrorStatus(t *testing.T) {
	cases := []struct {
		name string
		res  DispatchResult
		want int
	}{
		{"无可用渠道", DispatchResult{Err: ErrNoChannel}, http.StatusServiceUnavailable},
		{"限流透传", DispatchResult{Status: http.StatusTooManyRequests}, http.StatusTooManyRequests},
		{"请求问题透传", DispatchResult{Status: http.StatusBadRequest}, http.StatusBadRequest},
		{"上游故障透传", DispatchResult{Status: http.StatusServiceUnavailable}, http.StatusServiceUnavailable},
		{"渠道401不改写客户端语义", DispatchResult{Status: http.StatusUnauthorized}, http.StatusBadGateway},
		{"渠道403不改写客户端语义", DispatchResult{Status: http.StatusForbidden}, http.StatusBadGateway},
		{"传输失败", DispatchResult{Status: 0, Err: errors.New("dial tcp")}, http.StatusBadGateway},
	}
	for _, c := range cases {
		if got := groupErrorStatus(c.res); got != c.want {
			t.Errorf("%s: got=%d, 期望 %d", c.name, got, c.want)
		}
	}
}
