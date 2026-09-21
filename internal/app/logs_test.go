package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 编译期断言：statusWriter 必须实现 http.Flusher。
// Bug1 回归（PR#9/#10）：中间件包装层若吞掉 Flusher，handler 侧
// w.(http.Flusher) 断言失败，所有 SSE 流式响应将退化为缓冲一次性输出。
var _ http.Flusher = (*statusWriter)(nil)

func TestStatusWriterFlushDefaultsTo200(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec}
	sw.Flush() // 未写 header 前 Flush，不应 panic
	if sw.status != http.StatusOK {
		t.Fatalf("Flush on unset status should default to 200, got %d", sw.status)
	}
}

func TestStatusWriterFlushKeepsWrittenStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec}
	sw.WriteHeader(http.StatusTeapot)
	sw.Flush()
	if sw.status != http.StatusTeapot {
		t.Fatalf("Flush must not overwrite explicit status, got %d", sw.status)
	}
}

// Bug2 回归（PR#5）：非流式聚合日志处对 getNested 结果的断言必须安全，
// content 为 nil 或非 string 时不得 panic。
func TestNonStreamLogContentAssertionSafety(t *testing.T) {
	out := map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{"content": nil},
		}},
	}
	contentStr, _ := getNested(out, "choices", 0, "message", "content").(string)
	if contentStr != "" {
		t.Fatalf("expected empty string for nil content, got %q", contentStr)
	}
}

// statusWriter 透传 Write/WriteHeader 后底层 recorder 状态一致
func TestStatusWriterPassthroughWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec}
	sw.WriteHeader(http.StatusCreated)
	if _, err := sw.Write([]byte("ok")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if rec.Code != http.StatusCreated || rec.Body.String() != "ok" {
		t.Fatalf("passthrough broken: code=%d body=%q", rec.Code, rec.Body.String())
	}
}
