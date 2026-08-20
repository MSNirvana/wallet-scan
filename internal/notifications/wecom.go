package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"wallet-scan/internal/db"
)

// WeComClient sends read-only scanner notifications to a group bot.
type WeComClient struct {
	URL    string
	Client *http.Client
}

// NewWeComClient creates a bounded Webhook client.
func NewWeComClient(rawURL string, timeout time.Duration) *WeComClient {
	return &WeComClient{URL: rawURL, Client: &http.Client{Timeout: timeout}}
}

// SendPositive sends one address notification immediately.
func (c *WeComClient) SendPositive(ctx context.Context, view db.NotificationView) error {
	if c.URL == "" {
		return nil
	}
	content := "发现原生币余额\n\n地址：" + view.Address
	if view.Label != "" {
		content += "\n标签：" + view.Label
	}
	content += "\n\n余额："
	for _, finding := range view.Findings {
		content += "\n" + formatFinding(finding)
	}
	content += "\n\n查询时间：" + time.Now().Format(time.RFC3339)
	payload := map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": content}}
	return c.send(ctx, payload)
}

func formatFinding(finding db.PositiveView) string {
	decimals, atomicUnit := chainUnits(finding.Chain)
	display := formatAtomic(finding.Balance, decimals)
	return finding.Chain + "：" + display + " " + finding.AssetSymbol + "（最小单位：" + finding.Balance + " " + atomicUnit + "）"
}

func chainUnits(chain string) (int, string) {
	switch chain {
	case "btc":
		return 8, "satoshi"
	case "ethereum", "arbitrum", "bsc":
		return 18, "wei"
	case "solana":
		return 9, "lamport"
	case "tron":
		return 6, "sun"
	default:
		return 0, "atomic"
	}
}

func formatAtomic(raw string, decimals int) string {
	value := new(big.Int)
	if _, ok := value.SetString(raw, 10); !ok || decimals == 0 {
		return raw
	}
	digits := value.String()
	if len(digits) <= decimals {
		digits = strings.Repeat("0", decimals+1-len(digits)) + digits
	}
	point := len(digits) - decimals
	whole, fraction := digits[:point], strings.TrimRight(digits[point:], "0")
	if fraction == "" {
		return whole
	}
	return whole + "." + fraction
}

// SendNode sends one provider outage notification.
func (c *WeComClient) SendNode(ctx context.Context, view db.NodeNotificationView) error {
	if c.URL == "" {
		return nil
	}
	content := "节点异常\n\n链：" + view.Chain + "\n节点：" + view.Provider + "\n错误：" + view.ErrorCode
	content += "\n连续失败：" + fmt.Sprint(view.ConsecutiveFailures)
	content += "\n动作：暂停当前扫描批次\n时间：" + time.Now().Format(time.RFC3339)
	return c.send(ctx, map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": content}})
}

func (c *WeComClient) send(ctx context.Context, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode WeCom payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create WeCom request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.Client.Do(req)
	if err != nil {
		return fmt.Errorf("send WeCom request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("WeCom returned HTTP %d", response.StatusCode)
	}
	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result); err != nil {
		return fmt.Errorf("read WeCom response: %w", err)
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("WeCom error %d: %s", result.ErrCode, result.ErrMsg)
	}
	return nil
}
