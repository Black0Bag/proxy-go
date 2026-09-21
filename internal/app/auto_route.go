package app

// M2：能力分级 + auto 路由规则引擎（WP2.1/WP2.2）。
// 请求特征 → 硬性过滤（能力必须满足）→ tier 降序排序 → 交给 Balancer 轮询。
// 规则路由零额外 LLM 调用；LLM 分类器路由留作可选扩展（provider.ModelInfo.Tier 手工标注）。

import (
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
type ModelCaps struct {
	Tier          string // S/A/B/C（质量分级，S 最高）
	ContextWindow int
	Vision        bool
	ToolCall      bool
	Reasoning     bool
	CostPer1MIn   float64
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
