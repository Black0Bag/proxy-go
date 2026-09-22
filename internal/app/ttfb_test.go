package app

// M1 WP1.5-流式 单测：首字节（TTFB）边界。
//
// 用 httptest 搭真实上游（不 mock http.Client），覆盖三类语义：
//   - 首块必须被完整回放（不得丢字节）
//   - 上游返回 200 头后不吐正文 → TTFB 超时可换渠道，且能中止挂起的上游
//   - Dispatch + attemptStreaming 组合：挂起渠道自动换到正常渠道

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestTTFBTimeoutConfig 超时配置：默认 15s；非法值与非正值一律回退，不得退化为无超时。
func TestTTFBTimeoutConfig(t *testing.T) {
	t.Setenv("CLINE_PROXY_TTFB_TIMEOUT", "")
	if got := ttfbTimeout(); got != defaultTTFBTimeout {
		t.Errorf("未设置: got %v, 期望默认 %v", got, defaultTTFBTimeout)
	}
	t.Setenv("CLINE_PROXY_TTFB_TIMEOUT", "8s")
	if got := ttfbTimeout(); got != 8*time.Second {
		t.Errorf("合法值: got %v, 期望 8s", got)
	}
	for _, bad := range []string{"abc", "0", "-3s", "15"} {
		t.Setenv("CLINE_PROXY_TTFB_TIMEOUT", bad)
		if got := ttfbTimeout(); got != defaultTTFBTimeout {
			t.Errorf("非法值 %q: got %v, 期望回退 %v", bad, got, defaultTTFBTimeout)
		}
	}
}

// TestReadFirstChunkWithinTimeout 无数据上游必须在边界内返回 ErrTTFB，不能挂住。
func TestReadFirstChunkWithinTimeout(t *testing.T) {
	pr, _ := io.Pipe() // 永不写入
	defer pr.Close()

	start := time.Now()
	got, err := readFirstChunkWithin(context.Background(), pr, 150*time.Millisecond)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrTTFB) {
		t.Fatalf("err=%v, 期望 ErrTTFB", err)
	}
	if got != nil {
		t.Errorf("超时不应返回数据, got %q", got)
	}
	if elapsed > 2*time.Second {
		t.Errorf("超时未生效，耗时 %v", elapsed)
	}
}

// TestReadFirstChunkWithinData 有数据时立即返回首块。
func TestReadFirstChunkWithinData(t *testing.T) {
	got, err := readFirstChunkWithin(context.Background(), &oneShotReader{data: []byte("data: x\n\n")}, time.Second)
	if err != nil {
		t.Fatalf("err=%v, 期望 nil", err)
	}
	if string(got) != "data: x\n\n" {
		t.Errorf("got %q", got)
	}
}

// TestReadFirstChunkWithinCanceled 上游 ctx 取消时返回取消原因（而非 ErrTTFB），
// 使调用方能区分「客户端断开」与「上游超时」。
func TestReadFirstChunkWithinCanceled(t *testing.T) {
	pr, _ := io.Pipe()
	defer pr.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := readFirstChunkWithin(ctx, pr, 5*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, 期望 context.Canceled", err)
	}
}

// TestAttemptStreamingReplaysFirstChunk 关键回归守卫：TTFB 路径吃掉的首块必须完整回放，
// 否则客户端会丢掉流的前几个字节。
func TestAttemptStreamingReplaysFirstChunk(t *testing.T) {
	const chunk1, chunk2 = "data: a\n\n", "data: b\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(chunk1))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(chunk2))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resp, status, err := attemptStreaming(ctx, 3*time.Second, func(actx context.Context) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(actx, "POST", srv.URL, nil)
		return http.DefaultClient.Do(req)
	})
	if err != nil {
		t.Fatalf("err=%v, 期望 nil", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status=%d, 期望 200", status)
	}
	if _, ok := resp.Body.(*replayBody); !ok {
		t.Fatalf("Body=%T, 期望 *replayBody（首块已被 TTFB 路径缓冲）", resp.Body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取 body: %v", err)
	}
	if string(body) != chunk1+chunk2 {
		t.Errorf("body=%q, 期望 %q（首块丢失即回归 Bug1 类问题）", body, chunk1+chunk2)
	}
	if err := resp.Body.Close(); err != nil {
		t.Logf("close: %v", err)
	}
}

// TestAttemptStreamingTTFBAbortsHangingUpstream 上游发送 200 响应头后不吐正文：
// 必须在 TTFB 边界内放弃该渠道，并按传输错误上报（status 0）以便换渠道。
func TestAttemptStreamingTTFBAbortsHangingUpstream(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-release // 首块永不出现
	}))
	defer srv.Close()
	defer close(release) // LIFO：先释放 handler，再关服务器

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	resp, status, err := attemptStreaming(ctx, 200*time.Millisecond, func(actx context.Context) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(actx, "POST", srv.URL, nil)
		return http.DefaultClient.Do(req)
	})
	elapsed := time.Since(start)

	if resp != nil {
		t.Errorf("挂起的上游不应返回响应")
	}
	if status != 0 {
		t.Errorf("status=%d, 期望 0（按传输错误处理）", status)
	}
	if !errors.Is(err, ErrTTFB) {
		t.Fatalf("err=%v, 期望 ErrTTFB", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("耗时 %v，TTFB 超时未生效", elapsed)
	}
}

// TestAttemptStreamingNon2xx 上游非 2xx：返回状态码供 Dispatch 分类，不当作成功。
func TestAttemptStreamingNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"slow down"}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resp, status, err := attemptStreaming(ctx, time.Second, func(actx context.Context) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(actx, "POST", srv.URL, nil)
		return http.DefaultClient.Do(req)
	})
	if err != nil {
		t.Fatalf("err=%v, 期望 nil（422/429 等由 Dispatch 分类）", err)
	}
	if status != http.StatusTooManyRequests {
		t.Errorf("status=%d, 期望 429", status)
	}
	if resp != nil {
		t.Errorf("非 2xx 不应把响应交给调用方")
	}
}

// TestDispatchStreamingFailoverOnTTFB 组合语义：
// 组内首个渠道挂起（TTFB 超时）→ 自动换到下一个渠道并成功，客户端拿到完整流。
func TestDispatchStreamingFailoverOnTTFB(t *testing.T) {
	release := make(chan struct{})
	hanging := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-release
	}))
	defer hanging.Close()
	defer close(release)

	const want = "data: hello\n\ndata: world\n\n"
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(want))
	}))
	defer healthy.Close()

	target := map[string]string{"s1": hanging.URL, "s2": healthy.URL}

	b := NewBalancer()
	for _, id := range []string{"s1", "s2"} {
		b.UpsertChannel(Channel{ID: id, Name: id, Provider: "openai-compatible", APIKey: "k-" + id})
	}
	b.UpsertGroup(ModelGroup{Name: "g-stream", Members: []string{"s1", "s2"}, Strategy: "round_robin"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tried := []string{}
	var got *http.Response
	res := b.Dispatch(ctx, "g-stream", "", 0, func(ch *Channel) (int, error) {
		tried = append(tried, ch.ID)
		resp, status, err := attemptStreaming(ctx, 200*time.Millisecond, func(actx context.Context) (*http.Response, error) {
			req, _ := http.NewRequestWithContext(actx, "POST", target[ch.ID], nil)
			return http.DefaultClient.Do(req)
		})
		if err == nil && resp != nil {
			got = resp
		}
		return status, err
	})

	if res.Err != nil {
		t.Fatalf("res=%+v, 期望换渠道后成功", res)
	}
	if !res.Failover {
		t.Errorf("res.Failover=false, 期望发生换渠道（首渠道应 TTFB 超时）")
	}
	if len(tried) < 2 || tried[0] != "s1" {
		t.Fatalf("tried=%v, 期望先试挂起的 s1 再换渠道", tried)
	}
	if res.Channel == nil || res.Channel.ID != "s2" {
		t.Fatalf("最终渠道=%v, 期望 s2", res.Channel)
	}
	if got == nil {
		t.Fatalf("未拿到上游响应")
	}
	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("读取 body: %v", err)
	}
	if string(body) != want {
		t.Errorf("body=%q, 期望 %q", body, want)
	}
	_ = got.Body.Close()
}

// oneShotReader 一次性读取器：首读返回全部数据，之后 EOF。
type oneShotReader struct {
	data []byte
	done bool
}

func (r *oneShotReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	n := copy(p, r.data)
	return n, nil
}
