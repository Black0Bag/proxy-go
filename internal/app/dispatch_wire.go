package app

// M1 WP1.5-接线：把 Dispatch 接入主转发路径。
//
// 设计约束（与 docs/plan.md 一致）：
//  1. 默认不启用。开关 CLINE_PROXY_DISPATCH 未开启时主路径走既有 callClineAPI，
//     行为与接线前一致，可随时回退（灰度上线原则）。
//  2. 仅非流式请求接入。流式请求一旦向客户端写出首个字节就无法换渠道，
//     其 fallback 需要 TTFB 边界语义（ErrTTFB / NewTTFBContext），留待后续 WP 单独处理。
//  3. 账号池仍是凭据的唯一事实来源。Dispatch 前把当前 active 账号同步为专用组成员
//     （Account→Channel 映射），不改变账号池既有的选号策略与冷却机制。

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
)

// dispatchGroupName 账号池映射到渠道组时使用的专用组名。
// 双下划线前缀表示系统保留组，避免与 admin API 配置的业务组（对外模型名）冲突。
const dispatchGroupName = "__cline_pool__"

// dispatchEnabled 报告主路径是否启用 Dispatch（开关 CLINE_PROXY_DISPATCH）。
// 未设置或非真值时返回 false，主路径保持接线前的行为。
func dispatchEnabled() bool { return envTruthy("CLINE_PROXY_DISPATCH") }

// envTruthy 解析布尔型环境开关：1/true/on/yes（大小写与首尾空白不敏感）为真。
// 未设置、空值或其他取值一律为假——开关默认关闭是灰度上线的安全前提。
func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

// syncAccountsToBalancer 把账号池当前 active 账号同步为 dispatchGroupName 组成员，
// 返回同步后的候选数量。
//
// Channel.ID 复用 Account.AccountID，attempt 闭包内以 getAccountByID 反查回账号，
// 因此 Channel 中不落 token。非 active（cooldown/expired）账号不在成员列表中，
// 不会被 Pick 选中。
func syncAccountsToBalancer() int {
	return syncAccountsToBalancerInto(GlobalBalancer())
}

// syncAccountsToBalancerInto 是 syncAccountsToBalancer 的可注入版本，便于单测隔离全局单例。
func syncAccountsToBalancerInto(b *Balancer) int {
	p := loadPool()

	poolMu.Lock()
	accounts := make([]*Account, 0, len(p.Accounts))
	for _, a := range p.Accounts {
		if a == nil || a.Status != "active" {
			continue
		}
		accounts = append(accounts, a)
	}
	poolMu.Unlock()

	members := make([]string, 0, len(accounts))
	for _, a := range accounts {
		members = append(members, a.AccountID)
		b.UpsertChannel(Channel{
			ID:       a.AccountID,
			Name:     truncateEmail(a.Email),
			Provider: "cline",
		})
	}
	b.UpsertGroup(ModelGroup{Name: dispatchGroupName, Members: members})
	return len(members)
}

// upstreamStatusError 携带上游 HTTP 状态码的错误。
// Dispatch 需要按状态码区分「换渠道重试」与「直接返回」，而 callClineAPIOnAccount
// 失败时只返回 error；类型化错误让状态码可被 errors.As 取回，
// 同时 Error() 输出与原先的 fmt.Errorf("API %d: %s") 保持一致。
type upstreamStatusError struct {
	Status int
	Msg    string
}

func (e upstreamStatusError) Error() string { return e.Msg }

// upstreamStatusOf 从 error 中提取上游状态码；无法判定时返回 0（按传输错误处理，
// Dispatch 会据此换下一个渠道）。
func upstreamStatusOf(err error) int {
	var se upstreamStatusError
	if errors.As(err, &se) {
		return se.Status
	}
	return 0
}

// callClineAPIViaDispatch 经 Dispatch 选渠道后发射一次请求。
//
// 非流式：直接发射，非 2xx/传输错误均可换渠道重试。
// 流式（stream=true）：在 TTFB 边界内等待上游首块，首块到达即锁定渠道；
// 首块之前的任何失败（429/5xx/超时/挂起）都允许换账号，见 ttfb.go。
//
// sessionKey 为空时不启用渠道粘性，与账号池原先 round_robin 语义保持一致。
// 语义与 callClineAPI 对齐：成功返回上游响应与最终账号；失败返回最后一次尝试的账号与错误。
func callClineAPIViaDispatch(ctx context.Context, params map[string]any, stream bool, sessionKey string) (*http.Response, *Account, error) {
	if n := syncAccountsToBalancer(); n == 0 {
		return nil, nil, fmt.Errorf("no active accounts available: %s", describePoolStatus())
	}

	var (
		gotResp *http.Response
		gotAcc  *Account
	)
	res := GlobalBalancer().Dispatch(ctx, dispatchGroupName, sessionKey, 0, func(ch *Channel) (int, error) {
		acc := getAccountByID(ch.ID)
		if acc == nil {
			// 账号在同步与发射之间被移除：计传输错误，交给 Dispatch 换下一个
			return 0, fmt.Errorf("account %s vanished", ch.ID)
		}

		if !stream {
			resp, a, err := callClineAPIOnAccountCtx(ctx, params, false, acc)
			if err != nil {
				return upstreamStatusOf(err), err
			}
			gotResp, gotAcc = resp, a
			return resp.StatusCode, nil
		}

		// 流式：TTFB 边界内发射并等首块
		resp, status, err := attemptStreaming(ctx, ttfbTimeout(), func(actx context.Context) (*http.Response, error) {
			r, a, e := callClineAPIOnAccountCtx(actx, params, true, acc)
			if e == nil {
				gotAcc = a
			}
			return r, e
		})
		if err != nil {
			if errors.Is(err, ErrTTFB) {
				log.Printf("  dispatch: ttfb timeout on account=%s, trying next channel", truncateEmail(acc.Email))
			}
			return upstreamStatusOf(err), err
		}
		if resp != nil {
			gotResp = resp
		}
		return status, nil
	})

	if res.Err == nil && gotResp != nil {
		if res.Failover {
			log.Printf("  dispatch: failover recovered on account=%s attempts=%d",
				truncateEmail(gotAcc.Email), res.Attempts)
		}
		return gotResp, gotAcc, nil
	}
	if res.Err == nil {
		if res.Status != 0 {
			// 软失败（如 400/404）：Dispatch 未产生 err，补一个带状态码的错误保持信息量
			res.Err = upstreamStatusError{Status: res.Status, Msg: fmt.Sprintf("API %d", res.Status)}
		} else {
			res.Err = errors.New("dispatch finished without a response")
		}
	}
	log.Printf("  dispatch: all attempts failed (attempts=%d status=%d): %v", res.Attempts, res.Status, res.Err)
	return nil, gotAcc, res.Err
}
