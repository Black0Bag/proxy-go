package app

// M2：能力分级 + auto 路由规则引擎（WP2.1/WP2.2）。
// 请求特征 → 硬性过滤（能力必须满足）→ tier 降序排序 → 交给 Balancer 轮询。
// 规则路由零额外 LLM 调用；LLM 分类器路由留作可选扩展（provider.ModelInfo.Tier 手工标注）。

import (
	"fmt"
	"sort"
	"strings"
)

// RequestFeatures 从 OpenAI-compatible 请求体提取的路由特征。
type RequestFeatures struct {
	HasTools      bool // 请求含 tools/tool_choice/functions
	HasImage      bool // 消息含 image_url/图片
	EstContextTok int  // 估算上下文 token 数
	MaxTokens     int  // 请求 max_tokens（0=未指定）
	SessionKey    string
}

// ModelCaps 模型能力标签（渠道级冗余标注，与 provider.ModelInfo 对齐）。
// 可 JSON 序列化，随渠道一起落盘（data/channels.json 的 caps 字段）。
type ModelCaps struct {
	Tier          string  `json:"tier,omitempty"`           // S/A/B/C（质量分级，S 最高），空为未分级
	ContextWindow int     `json:"context_window,omitempty"` // 0 = 不限制
	Vision        bool    `json:"vision,omitempty"`
	ToolCall      bool    `json:"tool_call,omitempty"`
	Reasoning     bool    `json:"reasoning,omitempty"`
	CostPer1MIn   float64 `json:"cost_per_1m_in,omitempty"`
}

// annotatedCaps 把渠道的能力标注转为路由用 caps。
// 未标注（nil）时返回「宽松 caps」：不参与能力硬过滤（ToolCall/Vision 视为支持、
// 上下文不限），且 Tier 为空使其排序落在已标注渠道之后。
//
// 这样设计是为了避免「配了 auto 组但没标注能力 → 带 tools 的请求全被滤掉」这类
// 静默失效：未标注 = 能力未知但可用，而不是 = 不具备该能力。
func annotatedCaps(c *ModelCaps) ModelCaps {
	if c != nil {
		return *c
	}
	return ModelCaps{Tier: "", ToolCall: true, Vision: true}
}

// AutoCandidates 计算 auto 组候选顺序（M2 WP2.3 基础层）：
// 用请求特征对组内「存在且启用」的成员做能力硬过滤，再按 tier 降序排序。
// 返回候选渠道 ID 顺序（已过滤掉禁用成员）；无任何候选时返回 ErrNoChannel。
//
// 注意：熔断状态不在此处过滤（属动态健康状态，由 DispatchOrdered 在调度时判断），
// 因此候选列表在熔断变化后仍可复用。
func (b *Balancer) AutoCandidates(groupName string, feats RequestFeatures) ([]string, error) {
	b.mu.Lock()
	g, ok := b.groups[groupName]
	if !ok || len(g.Members) == 0 {
		b.mu.Unlock()
		return nil, fmt.Errorf("%w %q", ErrNoChannel, groupName)
	}
	entries := make(map[string]ModelCaps, len(g.Members))
	for _, id := range g.Members {
		c := b.channels[id]
		if c == nil || c.Disabled {
			continue
		}
		entries[id] = annotatedCaps(c.Caps)
	}
	b.mu.Unlock()

	cands := AutoRoute(feats, entries, nil)
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.ChannelID)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w %q: no member satisfies request capabilities", ErrNoChannel, groupName)
	}
	return out, nil
}

// tierRank tier 越高数值越大，排序用。
func tierRank(tier string) int {
	switch strings.ToUpper(tier) {
	case "S":
		return 4
	case "A":
		return 3
	case "B":
		return 2
	case "C":
		return 1
	}
	return 0
}

// candidate auto 路由的候选项。
type candidate struct {
	ChannelID string
	ModelName string // 映射后的上游模型名
	caps      ModelCaps
}

// AutoRoute 按请求特征过滤并排序候选渠道。
// entries: 渠道 ID → 其模型能力与上游模型名。
// 返回按 tier 降序（同 tier 按成本升序）的候选列表；能力不满足的被硬过滤。
func AutoRoute(feats RequestFeatures, entries map[string]ModelCaps, modelMap func(channelID string) string) []candidate {
	cands := make([]candidate, 0, len(entries))
	for id, caps := range entries {
		// 硬性能力过滤
		if feats.HasTools && !caps.ToolCall {
			continue
		}
		if feats.HasImage && !caps.Vision {
			continue
		}
		if feats.EstContextTok > 0 && caps.ContextWindow > 0 && feats.EstContextTok > caps.ContextWindow {
			continue
		}
		mapped := id
		if modelMap != nil {
			if m := modelMap(id); m != "" {
				mapped = m
			}
		}
		cands = append(cands, candidate{ChannelID: id, ModelName: mapped, caps: caps})
	}
	sort.Slice(cands, func(i, j int) bool {
		ri, rj := tierRank(cands[i].caps.Tier), tierRank(cands[j].caps.Tier)
		if ri != rj {
			return ri > rj
		}
		// 同 tier：短小请求优先便宜档
		if feats.EstContextTok > 0 && feats.EstContextTok < 8192 && feats.MaxTokens > 0 && feats.MaxTokens < 2048 {
			return cands[i].caps.CostPer1MIn < cands[j].caps.CostPer1MIn
		}
		return cands[i].ChannelID < cands[j].ChannelID
	})
	return cands
}

// EstimateContextTokens 粗估上下文 token 数（chars/3 中英混合经验值，足够路由用）。
func EstimateContextTokens(messages []map[string]any) int {
	total := 0
	for _, m := range messages {
		if c, ok := m["content"].(string); ok {
			total += len(c)
		}
	}
	return total / 3
}

// ExtractFeatures 从请求体 JSON map 提取特征。
func ExtractFeatures(body map[string]any, msgs []map[string]any) RequestFeatures {
	f := RequestFeatures{}
	if _, ok := body["tools"]; ok {
		f.HasTools = true
	}
	if _, ok := body["tool_choice"]; ok {
		f.HasTools = true
	}
	if _, ok := body["functions"]; ok {
		f.HasTools = true
	}
	for _, m := range msgs {
		if c, ok := m["content"].([]any); ok {
			for _, part := range c {
				if pm, ok := part.(map[string]any); ok {
					if t, _ := pm["type"].(string); t == "image_url" {
						f.HasImage = true
					}
				}
			}
		}
	}
	f.EstContextTok = EstimateContextTokens(msgs)
	if mt, ok := body["max_tokens"].(float64); ok {
		f.MaxTokens = int(mt)
	}
	return f
}
