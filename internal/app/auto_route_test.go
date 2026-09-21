package app

import (
	"testing"
)

func TestAutoRouteFiltersTools(t *testing.T) {
	entries := map[string]ModelCaps{
		"a": {Tier: "S", ToolCall: true},
		"b": {Tier: "A", ToolCall: false},
	}
	got := AutoRoute(RequestFeatures{HasTools: true}, entries, nil)
	if len(got) != 1 || got[0].ChannelID != "a" {
		t.Fatalf("tool filter broken: %+v", got)
	}
}

func TestAutoRouteFiltersVision(t *testing.T) {
	entries := map[string]ModelCaps{
		"a": {Tier: "S", Vision: false},
		"b": {Tier: "B", Vision: true},
	}
	got := AutoRoute(RequestFeatures{HasImage: true}, entries, nil)
	if len(got) != 1 || got[0].ChannelID != "b" {
		t.Fatalf("vision filter broken: %+v", got)
	}
}

func TestAutoRouteFiltersContextWindow(t *testing.T) {
	entries := map[string]ModelCaps{
		"a": {Tier: "S", ContextWindow: 8192},
		"b": {Tier: "A", ContextWindow: 131072},
	}
	got := AutoRoute(RequestFeatures{EstContextTok: 32000}, entries, nil)
	if len(got) != 1 || got[0].ChannelID != "b" {
		t.Fatalf("context filter broken: %+v", got)
	}
}

func TestAutoRouteTierOrder(t *testing.T) {
	entries := map[string]ModelCaps{
		"c-tierB": {Tier: "B", ToolCall: true},
		"a-tierS": {Tier: "S", ToolCall: true},
		"b-tierA": {Tier: "A", ToolCall: true},
	}
	got := AutoRoute(RequestFeatures{HasTools: true}, entries, nil)
	if len(got) != 3 {
		t.Fatalf("expect 3, got %d", len(got))
	}
	if got[0].ChannelID != "a-tierS" || got[1].ChannelID != "b-tierA" || got[2].ChannelID != "c-tierB" {
		t.Fatalf("tier order broken: %s,%s,%s", got[0].ChannelID, got[1].ChannelID, got[2].ChannelID)
	}
}

func TestAutoRouteShortRequestPrefersCheap(t *testing.T) {
	entries := map[string]ModelCaps{
		"expensive": {Tier: "A", ToolCall: true, CostPer1MIn: 10.0},
		"cheap":     {Tier: "A", ToolCall: true, CostPer1MIn: 0.5},
	}
	got := AutoRoute(RequestFeatures{HasTools: true, EstContextTok: 1000, MaxTokens: 500}, entries, nil)
	if got[0].ChannelID != "cheap" {
		t.Fatalf("short+small should prefer cheap, got %s", got[0].ChannelID)
	}
}

func TestExtractFeatures(t *testing.T) {
	body := map[string]any{
		"tools":      []any{},
		"max_tokens": float64(1024),
	}
	msgs := []map[string]any{
		{"content": "hello world this is a long enough message"},
		{"content": []any{map[string]any{"type": "image_url"}}},
	}
	f := ExtractFeatures(body, msgs)
	if !f.HasTools || !f.HasImage || f.MaxTokens != 1024 || f.EstContextTok == 0 {
		t.Fatalf("extract broken: %+v", f)
	}
}
