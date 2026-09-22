package app

// M2 WP2.3b 渠道组转发通路：把 admin 配置的渠道组接入主转发路径。
//
// 背景（WP2.3b 前置侦察结论）：主路径的 model 此前只分 zen 与 Cline 账号池两类，
// data/channels.json 里由 admin 配置的渠道组从未被主转发路径消费；
// provider.Provider 亦只有 Name/ListModels/ProbeBalance，没有 Chat 转发。
// 本文件补上「渠道组 → OpenAI 兼容上游」的转发实现。
//
// 设计约束：
//  1. 默认关闭：仅当 CLINE_PROXY_GROUP_ROUTING 开启且 model 命中已配置组名时生效，
//     关闭时主路径行为与接线前完全一致，可随时回退。
//  2. 组名即「对外模型名」：显式配置的组优先于 zen/Cline 内置路由（显式配置优先原则）。
//  3. strategy=auto → 能力过滤 + tier 排序 + 会话粘性（AutoCandidates + DispatchOrdered）；
//     其余策略复用 M1 的 Dispatch（round_robin/weighted/least_used/lowest_latency）。
//  4. 上游统一按 OpenAI 兼容协议转发：BaseURL + /chat/completions + Bearer Key + ModelMap。
//  5. 流式复用 ttfb.go 的首字节边界语义：首块到达前允许换渠道，之后锁定。
//  6. 响应为纯字节透传（不做协议转换、不做用量记账）；组渠道用量统计留待后续 WP。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"cline-go-proxy/internal/kit"
)

// groupRoutingEnv 渠道组转发开关的环境变量名。
const groupRoutingEnv = "CLINE_PROXY_GROUP_ROUTING"

// groupRoutingEnabled 报告渠道组转发是否启用。
func groupRoutingEnabled() bool { return envTruthy(groupRoutingEnv) }

// matchGroupName 查找与 model 同名的可路由渠道组（组名即对外模型名）。
// b 可注入以便单测；生产路径传 GlobalBalancer()。
func matchGroupName(b *Balancer, model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" || b == nil {
		return "", false
	}
	g, ok := b.Group(model)
	if !ok || len(g.Members) == 0 {
		return "", false
	}
	return g.Name, true
}

// groupSessionKey 会话指纹：同一会话稳定命中同一渠道（auto 组粘性）。
//
// 取对话前两条消息（system + 首条 user）做 SHA-256 前缀：Cline 类客户端每次携带完整
// 对话历史，前缀在会话内稳定，因此同一会话持续锁定同一渠道，且不依赖客户端额外请求头。
// 截取 8 字节（16 hex 字符）足够作 key，日志里也不至于过长。
func groupSessionKey(params map[string]any) string {
	msgs, ok := params["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return ""
	}
	h := sha256.New()
	wrote := false
	for i := range min(len(msgs), 2) {
		b, err := json.Marshal(msgs[i])
		if err != nil {
			continue
		}
		h.Write(b)
		h.Write([]byte{0}) // 分隔符，避免 ["a","b"] 与 ["ab"] 碰撞
		wrote = true
	}
	if !wrote {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// uploadMessages 把 params["messages"]（[]any）转为 ExtractFeatures 需要的 []map[string]any。
func groupMessages(params map[string]any) []map[string]any {
	raw, ok := params["messages"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		if mm, ok := m.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

// emitChannelRequest 向上游渠道发射一次 OpenAI 兼容 chat/completions 请求。
//
// 所有 HTTP 状态码都通过 response 返回（调用方按状态分类重试或回传）；
// 仅传输层错误（建连失败、超时、ctx 取消）返回 error。
// params 只读，不得被本函数修改；模型映射在副本上完成，失败重试到其他渠道时会重新映射。
func emitChannelRequest(ctx context.Context, ch *Channel, params map[string]any, stream bool) (*http.Response, error) {
	base := strings.TrimRight(strings.TrimSpace(ch.BaseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("channel %s: base_url is empty", ch.ID)
	}

	body := make(map[string]any, len(params)+1)
	for k, v := range params {
		body[k] = v
	}
	if m, _ := body["model"].(string); m != "" {
		if mapped, ok := ch.ModelMap[m]; ok && mapped != "" {
			body["model"] = mapped
		}
	}
	body["stream"] = stream

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("channel %s: marshal body: %w", ch.ID, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("channel %s: create request: %w", ch.ID, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if ch.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+ch.APIKey)
	}

	resp, err := kit.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("channel %s: %w", ch.ID, err)
	}
	return resp, nil
}

// handleGroupChat 渠道组转发主入口（OpenAI 兼容协议；由主路径在开关命中时调用）。
func handleGroupChat(w http.ResponseWriter, r *http.Request, params map[string]any, groupName string, isStream bool) {
	handleGroupChatWith(w, r, GlobalBalancer(), params, groupName, isStream)
}

// handleGroupChatWith 是 handleGroupChat 的可注入版本（b 可替换，便于单测隔离全局单例）。
func handleGroupChatWith(w http.ResponseWriter, r *http.Request, b *Balancer, params map[string]any, groupName string, isStream bool) {
	ctx := r.Context()

	g, ok := b.Group(groupName)
	if !ok || len(g.Members) == 0 {
		writeJSON(w, http.StatusBadGateway, apiErrorBody(fmt.Sprintf("channel group %q is not configured", groupName)))
		return
	}
	log.Printf("  group %q: dispatch model=%v stream=%v strategy=%s members=%d",
		groupName, params["model"], isStream, g.Strategy, len(g.Members))

	var gotResp *http.Response
	attempt := func(ch *Channel) (int, error) {
		if isStream {
			// 流式：首块到达前允许换渠道（TTFB 边界），首块后锁定
			resp, status, err := attemptStreaming(ctx, ttfbTimeout(), func(actx context.Context) (*http.Response, error) {
				return emitChannelRequest(actx, ch, params, true)
			})
			if err != nil {
				return upstreamStatusOf(err), err
			}
			if resp != nil {
				gotResp = resp // 必须取 attemptStreaming 包装后的响应（首块已回放进 Body）
			}
			return status, nil
		}

		resp, err := emitChannelRequest(ctx, ch, params, false)
		if err != nil {
			return upstreamStatusOf(err), err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			gotResp = resp
			return resp.StatusCode, nil
		}
		// 非 2xx：读完并关闭正文，构造带状态码的错误（文本风格与 Cline 路径一致）
		bodyText := kit.Truncate(kit.ReadBody(resp), 500)
		_ = resp.Body.Close()
		return resp.StatusCode, upstreamStatusError{
			Status: resp.StatusCode,
			Msg:    fmt.Sprintf("API %d: %s", resp.StatusCode, bodyText),
		}
	}

	var res DispatchResult
	if g.Strategy == "auto" {
		// auto 组：按请求特征过滤能力并降序排序（WP2.3a），再走按序调度
		feats := ExtractFeatures(params, groupMessages(params))
		cands, err := b.AutoCandidates(groupName, feats)
		if err != nil {
			log.Printf("  group %q: no candidate satisfies request: %v", groupName, err)
			writeJSON(w, http.StatusServiceUnavailable, apiErrorBody(err.Error()))
			return
		}
		res = b.DispatchOrdered(ctx, cands, groupSessionKey(params), 0, attempt)
	} else {
		// 显式策略组：完整复用 M1 调度语义（轮询/权重/最少使用/最低延迟 + 熔断 + 粘性）
		res = b.Dispatch(ctx, groupName, groupSessionKey(params), 0, attempt)
	}

	if res.Err != nil || gotResp == nil {
		status := groupErrorStatus(res)
		msg := "no available channel in group"
		if res.Err != nil {
			msg = res.Err.Error()
		} else if res.Status != 0 {
			msg = fmt.Sprintf("API %d", res.Status)
		}
		log.Printf("  group %q: upstream failed (attempts=%d status=%d): %s", groupName, res.Attempts, res.Status, msg)
		writeJSON(w, status, apiErrorBody(msg))
		return
	}
	defer gotResp.Body.Close()

	channelID := ""
	if res.Channel != nil {
		channelID = res.Channel.ID
	}
	if res.Failover {
		log.Printf("  group %q: failover recovered on channel=%s attempts=%d", groupName, channelID, res.Attempts)
	} else {
		log.Printf("  group %q: upstream ok channel=%s stream=%v", groupName, channelID, isStream)
	}

	if isStream {
		passthroughStream(w, gotResp)
		return
	}
	passthroughNonStream(w, gotResp)
}

// groupErrorStatus 决定失败时回传给客户端的状态码。
//
//   - 组内无可用渠道（ErrNoChannel）→ 503：服务侧容量问题，客户端可稍后重试
//   - 429/400/422/5xx：对客户端有明确语义（限流 / 请求问题 / 上游故障）→ 原样回传
//   - 401/403：属渠道凭据问题（客户端对代理的鉴权此前已通过）→ 502，避免误导客户端改自己的 key
//   - 其余（含无状态码的传输失败）→ 502
func groupErrorStatus(res DispatchResult) int {
	if errors.Is(res.Err, ErrNoChannel) {
		return http.StatusServiceUnavailable
	}
	switch {
	case res.Status == http.StatusTooManyRequests,
		res.Status == http.StatusBadRequest,
		res.Status == http.StatusUnprocessableEntity,
		res.Status >= 500 && res.Status <= 599:
		return res.Status
	default:
		return http.StatusBadGateway
	}
}

// apiErrorBody 构造与既有链路一致的错误体。
func apiErrorBody(msg string) map[string]any {
	return map[string]any{"error": map[string]string{"message": msg, "type": "api_error"}}
}

// passthroughStream 把上游 SSE 字节流逐块转发并 flush（纯透传，不解析内容）。
// 客户端不支持 Flusher 时退化为聚合后一次性写出，保证字节完整。
func passthroughStream(w http.ResponseWriter, upstream *http.Response) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		data, err := io.ReadAll(upstream.Body)
		if err != nil {
			log.Printf("  stream: read upstream failed: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(data); err != nil {
			log.Printf("  stream: write aggregated failed: %v", err)
		}
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	flusher.Flush() // 尽快把响应头推给客户端，避免其误判超时

	buf := make([]byte, 32*1024)
	for {
		n, err := upstream.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				log.Printf("  stream: client write failed (client gone?): %v", werr)
				return
			}
			flusher.Flush()
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("  stream: upstream read ended: %v", err)
			}
			return
		}
	}
}

// passthroughNonStream 原样转发非流式响应（状态码 + Content-Type + 正文）。
func passthroughNonStream(w http.ResponseWriter, upstream *http.Response) {
	if ct := upstream.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(upstream.StatusCode)
	if _, err := io.Copy(w, upstream.Body); err != nil {
		log.Printf("  group: copy response body failed: %v", err)
	}
}
