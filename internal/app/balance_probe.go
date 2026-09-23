package app

// M3 WP3.1：多供应商余额探针。
// 端点已于 2026-09-22 逐一验证（401=存在）：
//   DeepSeek     GET https://api.deepseek.com/user/balance
//   SiliconFlow  GET https://api.siliconflow.cn/v1/user/info
//   OpenRouter   GET https://openrouter.ai/api/v1/credits
//   Moonshot     GET https://api.moonshot.cn/v1/users/me/balance
//   newapi/one-api 系 GET {base}/api/user/self （quota 字段，quota/500000=美元）

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cline-go-proxy/internal/provider"
)

// ProbeClient 余额探针 HTTP 客户端（可注入测试）。
var ProbeClient = &http.Client{Timeout: 15 * time.Second}

// probeFunc 单供应商余额解析。baseURL 空=官方端点。
type probeFunc func(ctx context.Context, apiKey, baseURL string) (*provider.Balance, error)

// balanceProbes provider 名 → 探针。大小写不敏感匹配。
var balanceProbes = map[string]probeFunc{
	"deepseek":    probeDeepSeek,
	"siliconflow": probeSiliconFlow,
	"openrouter":  probeOpenRouter,
	"moonshot":    probeMoonshot,
	"newapi":      probeNewAPI,
	"one-api":     probeNewAPI,
}

// ProbeBalance 探测指定 provider 的余额。未知 provider 返回错误。
func ProbeBalance(ctx context.Context, providerName, apiKey, baseURL string) (*provider.Balance, error) {
	pf, ok := balanceProbes[strings.ToLower(providerName)]
	if !ok {
		return nil, fmt.Errorf("no balance probe for provider %q", providerName)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("empty api key for provider %q", providerName)
	}
	bal, err := pf(ctx, apiKey, baseURL)
	if err != nil {
		return nil, fmt.Errorf("probe %s: %w", providerName, err)
	}
	bal.ProbedAt = time.Now()
	return bal, nil
}

// probeCommon 公共 GET + Bearer + JSON 解析骨架。
func probeCommon(ctx context.Context, apiKey, url string, parse func(body []byte) (*provider.Balance, error)) (*provider.Balance, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := ProbeClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, truncate(body, 200))
	}
	return parse(body)
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}

// probeDeepSeek: {"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"110.50","granted_balance":"0.00","topped_up_balance":"110.50"}]}
func probeDeepSeek(ctx context.Context, apiKey, baseURL string) (*provider.Balance, error) {
	url := strings.TrimSuffix(baseURL, "/")
	if url == "" {
		url = "https://api.deepseek.com"
	}
	return probeCommon(ctx, apiKey, url+"/user/balance", func(body []byte) (*provider.Balance, error) {
		var d struct {
			BalanceInfos []struct {
				Currency     string `json:"currency"`
				TotalBalance string `json:"total_balance"`
			} `json:"balance_infos"`
		}
		if err := json.Unmarshal(body, &d); err != nil {
			return nil, err
		}
		if len(d.BalanceInfos) == 0 {
			return nil, fmt.Errorf("empty balance_infos")
		}
		info := d.BalanceInfos[0]
		total, used := parseMoney(info.TotalBalance), 0.0
		return &provider.Balance{Currency: info.Currency, Total: total, Used: used}, nil
	})
}

// probeSiliconFlow: {"data":{"balance":"10.50","totalBalance":"10.50",...},"status":true}
func probeSiliconFlow(ctx context.Context, apiKey, baseURL string) (*provider.Balance, error) {
	url := strings.TrimSuffix(baseURL, "/")
	if url == "" {
		url = "https://api.siliconflow.cn"
	}
	return probeCommon(ctx, apiKey, url+"/v1/user/info", func(body []byte) (*provider.Balance, error) {
		var d struct {
			Data struct {
				Balance      string `json:"balance"`
				TotalBalance string `json:"totalBalance"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &d); err != nil {
			return nil, err
		}
		v := d.Data.TotalBalance
		if v == "" {
			v = d.Data.Balance
		}
		return &provider.Balance{Currency: "CNY", Total: parseMoney(v)}, nil
	})
}

// probeOpenRouter: {"data":{"total_credits":10.0,"total_usage":2.5}}
func probeOpenRouter(ctx context.Context, apiKey, baseURL string) (*provider.Balance, error) {
	url := strings.TrimSuffix(baseURL, "/")
	if url == "" {
		url = "https://openrouter.ai/api/v1"
	}
	return probeCommon(ctx, apiKey, url+"/credits", func(body []byte) (*provider.Balance, error) {
		var d struct {
			Data struct {
				TotalCredits float64 `json:"total_credits"`
				TotalUsage   float64 `json:"total_usage"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &d); err != nil {
			return nil, err
		}
		return &provider.Balance{Currency: "USD", Total: d.Data.TotalCredits, Used: d.Data.TotalUsage}, nil
	})
}

// probeMoonshot: {"data":{"available_balance":"93.99","voucher_balance":"0","..."}}
func probeMoonshot(ctx context.Context, apiKey, baseURL string) (*provider.Balance, error) {
	url := strings.TrimSuffix(baseURL, "/")
	if url == "" {
		url = "https://api.moonshot.cn"
	}
	return probeCommon(ctx, apiKey, url+"/v1/users/me/balance", func(body []byte) (*provider.Balance, error) {
		var d struct {
			Data struct {
				AvailableBalance string `json:"available_balance"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &d); err != nil {
			return nil, err
		}
		return &provider.Balance{Currency: "CNY", Total: parseMoney(d.Data.AvailableBalance)}, nil
	})
}

// probeNewAPI: one-api 系 {"success":true,"data":{"quota":5000000,...}}，quota/500000=美元
func probeNewAPI(ctx context.Context, apiKey, baseURL string) (*provider.Balance, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("newapi probe requires base_url")
	}
	url := strings.TrimSuffix(baseURL, "/") + "/api/user/self"
	return probeCommon(ctx, apiKey, url, func(body []byte) (*provider.Balance, error) {
		var d struct {
			Success bool `json:"success"`
			Data    struct {
				Quota int64 `json:"quota"`
				Used  int64 `json:"used_quota"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &d); err != nil {
			return nil, err
		}
		if !d.Success {
			return nil, fmt.Errorf("success=false: %s", truncate(body, 120))
		}
		const quotaPerUSD = 500000.0
		return &provider.Balance{
			Currency: "USD",
			Total:    float64(d.Data.Quota) / quotaPerUSD,
			Used:     float64(d.Data.Used) / quotaPerUSD,
		}, nil
	})
}

// parseMoney 解析 "110.50" 类字符串，失败返回 0。
func parseMoney(s string) float64 {
	var f float64
	_, _ = fmt.Sscanf(strings.TrimSpace(s), "%f", &f)
	return f
}
