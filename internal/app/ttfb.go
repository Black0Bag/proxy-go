package app

// M1 WP1.5-流式：首字节（TTFB）边界保护。
//
// 语义：只在「上游首个数据块到达之前」允许 Dispatch 换渠道。
// 一旦拿到首块，立刻锁定该渠道——此后任何失败都不再重试，
// 因为客户端即将（或已经）收到字节，重试会造成重复输出。
//
// 覆盖两种真实故障：
//  1. 上游连接建立但迟迟不返回响应头 → client.Do 阻塞（ctx 超时兜住）
//  2. 上游返回 200 响应头后不吐任何正文 → 首块读取超时（本文件兜住）

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// defaultTTFBTimeout 上游首字节等待上限（plan.md WP1.5：15s）。
const defaultTTFBTimeout = 15 * time.Second

// ttfbTimeout 读取首字节超时配置 CLINE_PROXY_TTFB_TIMEOUT（Go duration 字符串，如 "8s"）。
//
// 非法值或非正值一律回退默认 15s——开关打开时不允许退化成「无超时」，
// 否则一个挂死的上游会永久占用请求。
func ttfbTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("CLINE_PROXY_TTFB_TIMEOUT"))
	if raw == "" {
		return defaultTTFBTimeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Printf("  ttfb: invalid CLINE_PROXY_TTFB_TIMEOUT=%q, fallback to %v", raw, defaultTTFBTimeout)
		return defaultTTFBTimeout
	}
	return d
}

// replayBody 把已读出的首块拼回上游 Body 之前，使调用方看到完整字节流。
//
// Close 负责释放 TTFB 上下文：成功路径下该上下文必须存活到流结束
// （提前 cancel 会掐断正在传输的响应），因此只能在这里回收。
type replayBody struct {
	first  []byte
	inner  io.ReadCloser
	cancel context.CancelFunc
}

func (b *replayBody) Read(p []byte) (int, error) {
	if len(b.first) > 0 {
		n := copy(p, b.first)
		b.first = b.first[n:]
		return n, nil
	}
	return b.inner.Read(p)
}

func (b *replayBody) Close() error {
	err := b.inner.Close()
	if b.cancel != nil {
		b.cancel()
	}
	return err
}

// readFirstChunkWithin 在 d 内读取首个非空数据块。
// 超时返回 ErrTTFB；ctx 取消返回 ctx.Err()；上游直接结束返回 io.EOF 等原始错误。
//
// 超时/取消路径不等待读 goroutine 收尾：调用方随后 cancel + Close body
// 会让阻塞中的 Read 立即返回，goroutine 向带缓冲 channel 投递后自行退出，不泄漏。
func readFirstChunkWithin(ctx context.Context, body io.Reader, d time.Duration) ([]byte, error) {
	type chunk struct {
		data []byte
		err  error
	}
	ch := make(chan chunk, 1)
	buf := make([]byte, 4096)
	go func() {
		for {
			n, err := body.Read(buf)
			// 容忍 (0, nil)：io.Reader 允许，继续读直到有数据或出错误
			if n > 0 || err != nil {
				ch <- chunk{data: buf[:n], err: err}
				return
			}
		}
	}()

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case c := <-ch:
		if len(c.data) > 0 {
			return c.data, nil
		}
		if c.err == nil {
			c.err = io.EOF
		}
		return nil, c.err
	case <-timer.C:
		return nil, ErrTTFB
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// attemptStreaming 执行一次流式 attempt：发射请求 + 在 TTFB 边界内等待首块。
//
// 返回 (resp, status, err)：
//   - 成功：err==nil、status 为上游 2xx；resp.Body 已包装为 replayBody，可完整读取
//   - 上游非 2xx：err==nil、status 为该状态码（交 Dispatch 分类），resp 为 nil
//   - 传输错误 / TTFB 超时：err!=nil、status 由 upstreamStatusOf 提取（超时为 0）
//
// emitFn 注入发射动作（生产为 callClineAPIOnAccountCtx 闭包），便于用 httptest 单测。
func attemptStreaming(ctx context.Context, ttfb time.Duration, emitFn func(context.Context) (*http.Response, error)) (*http.Response, int, error) {
	ttfbtx, cancel := context.WithCancel(ctx)
	resp, err := emitFn(ttfbtx)
	if err != nil {
		cancel()
		return nil, upstreamStatusOf(err), err
	}
	if resp == nil {
		cancel()
		return nil, 0, errors.New("emit returned nil response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 非 2xx：body 已由发射函数读取并关闭，这里再关一次是幂等的
		_ = resp.Body.Close()
		cancel()
		return nil, resp.StatusCode, nil
	}

	first, ferr := readFirstChunkWithin(ttfbtx, resp.Body, ttfb)
	if len(first) > 0 {
		// 首块到手：锁定渠道，上下文继续存活到流结束（由 replayBody.Close 回收）
		resp.Body = &replayBody{first: first, inner: resp.Body, cancel: cancel}
		return resp, resp.StatusCode, nil
	}

	// 首块失败：释放资源并按传输错误上报，交给 Dispatch 换渠道
	_ = resp.Body.Close()
	cancel()
	if ferr == nil {
		ferr = ErrTTFB
	} else if errors.Is(ferr, io.EOF) {
		// 200 但正文为空：判为上游异常（无内容可流），同样允许换渠道
		ferr = fmt.Errorf("%w: empty upstream body", ErrTTFB)
	}
	return nil, 0, ferr
}
