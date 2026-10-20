package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const DefaultDelayURL = "https://www.gstatic.com/generate_204"
const proxyDelayTimeout = 5 * time.Second

func (s Settings) EffectiveDelayURL() string {
	if s.DelayURL == "" {
		return DefaultDelayURL
	}
	return s.DelayURL
}

func (c *Controller) Delay(ctx context.Context, proxy, target string) (int, error) {
	if proxy == "" {
		return 0, fmt.Errorf("没有可测试的节点")
	}
	if target == "" {
		target = DefaultDelayURL
	}
	if err := validateURL(target); err != nil {
		return 0, fmt.Errorf("延迟测试地址必须为有效 HTTP/HTTPS URL")
	}
	query := url.Values{"url": {target}, "timeout": {"5000"}}
	var result struct {
		Delay *int `json:"delay"`
	}
	// 普通控制请求的 3 秒超时短于内核测速窗口，测速单独预留响应时间。
	client := *c.Client
	client.Timeout = proxyDelayTimeout + time.Second
	controller := *c
	controller.Client = &client
	ctx, cancel := context.WithTimeout(ctx, proxyDelayTimeout+time.Second)
	defer cancel()
	err := controller.Do(ctx, http.MethodGet, "/proxies/"+url.PathEscape(proxy)+"/delay?"+query.Encode(), nil, &result)
	if err != nil {
		var status *ControllerHTTPError
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &status) && status.Status == http.StatusGatewayTimeout) {
			return 0, fmt.Errorf("延迟测试超时: %w", context.DeadlineExceeded)
		}
		if errors.As(err, &status) && status.Status == http.StatusServiceUnavailable {
			return 0, fmt.Errorf("节点无法完成测试请求（HTTP 503）")
		}
		return 0, err
	}
	if result.Delay == nil || *result.Delay <= 0 {
		return 0, fmt.Errorf("内核未返回有效延迟")
	}
	return *result.Delay, nil
}

func (m *Manager) ProxyDelay(ctx context.Context, proxy string) (int, error) {
	c, err := m.runtimeController()
	if err != nil {
		return 0, err
	}
	s, err := m.Store.Settings()
	if err != nil {
		return 0, err
	}
	return c.Delay(ctx, proxy, s.EffectiveDelayURL())
}
