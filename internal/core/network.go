package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type IPProbe struct {
	Label    string
	URL      string
	IPv4Only bool
}

func PublicIPProbes() []IPProbe {
	return []IPProbe{
		{Label: "中国", URL: "https://my.ip.cn/", IPv4Only: true},
		{Label: "Cloudflare", URL: "https://1.1.1.1/cdn-cgi/trace"},
		{Label: "Claude AI", URL: "https://claude.ai/cdn-cgi/trace"},
	}
}

var chinaIPPattern = regexp.MustCompile(`(?im)^\s*ip[：:]\s*((?:[0-9]{1,3}\.){3}[0-9]{1,3})(?:\s|$)`)

func proxyURL(d *Document) (*url.URL, error) {
	host := d.Text("bind-address")
	if host == "" || host == "*" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	host = strings.Trim(host, "[]")
	for _, key := range []string{"mixed-port", "port", "socks-port"} {
		port, e := strconv.Atoi(d.Text(key))
		if e != nil || port == 0 {
			continue
		}
		scheme := "http"
		if key == "socks-port" {
			scheme = "socks5h"
		}
		u := &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, strconv.Itoa(port))}
		if a := d.Get("authentication"); a != nil && len(a.Content) > 0 {
			user, pass, ok := strings.Cut(a.Content[0].Value, ":")
			if !ok {
				return nil, fmt.Errorf("代理认证格式无效")
			}
			u.User = url.UserPassword(user, pass)
		}
		return u, nil
	}
	return nil, fmt.Errorf("需要开启 Mixed、HTTP 或 SOCKS 端口才能通过代理查询")
}
func proxyHTTPClient(d *Document) (*http.Client, error) {
	u, err := proxyURL(d)
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(u)}}, nil
}
func (m *Manager) ProxyClient(ctx context.Context) (*http.Client, error) {
	st, err := m.Status(ctx)
	if err != nil {
		return nil, err
	}
	if !st.Running {
		return nil, fmt.Errorf("请先启动代理，启动完成后才能访问 GitHub")
	}
	_, live, err := m.process()
	if err != nil {
		return nil, err
	}
	if !live {
		return nil, fmt.Errorf("代理已停止")
	}
	return m.runtimeProxyClient()
}

func (m *Manager) runtimeProxyClient() (*http.Client, error) {
	raw, err := os.ReadFile(m.Store.Path("config.yaml"))
	if err != nil {
		return nil, err
	}
	d, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	return proxyHTTPClient(d)
}
func QueryPublicIP(ctx context.Context, client *http.Client, probe IPProbe) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", probe.URL, nil)
	if err != nil {
		return "", err
	}
	// 重定向可能命中另一条分流规则，不能冒充原探测目标的出口。
	probeClient := *client
	probeClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return fmt.Errorf("IP 探测不接受重定向")
	}
	res, err := probeClient.Do(req)
	if err != nil {
		return "", safeNetworkError(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("IP 查询 HTTP %d", res.StatusCode)
	}
	b, err := readBounded(res.Body, 64<<10)
	if err != nil {
		return "", err
	}
	return parseProbeIP(b, probe.IPv4Only)
}

func parseProbeIP(body []byte, ipv4Only bool) (string, error) {
	address := ""
	if ipv4Only {
		if match := chinaIPPattern.FindSubmatch(body); match != nil {
			address = string(match[1])
		}
	} else {
		found := false
		for _, line := range strings.Split(string(body), "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
			if ok && key == "ip" {
				if found {
					return "", fmt.Errorf("IP 查询返回重复地址字段")
				}
				found = true
				address = strings.TrimSpace(value)
			}
		}
	}
	ip := net.ParseIP(address)
	if ip == nil || (ipv4Only && ip.To4() == nil) {
		return "", fmt.Errorf("IP 查询返回无效地址")
	}
	return ip.String(), nil
}
func (m *Manager) PublicIP(ctx context.Context, probe IPProbe) (string, error) {
	_, live, err := m.process()
	if err != nil {
		return "", err
	}
	if !live {
		return QueryPublicIP(ctx, m.HTTP, probe)
	}
	client, err := m.runtimeProxyClient()
	if err != nil {
		return "", err
	}
	defer client.CloseIdleConnections()
	return QueryPublicIP(ctx, client, probe)
}
func VersionTag(v string) string {
	for _, field := range strings.Fields(v) {
		if strings.HasPrefix(field, "v") {
			return field
		}
	}
	return strings.TrimSpace(v)
}
func (m *Manager) CurrentVersion(ctx context.Context) (string, error) {
	p, live, err := m.process()
	if err != nil {
		return "", err
	}
	binary := p.Binary
	if !live {
		binary, err = m.Store.Kernel()
		if err != nil {
			return "", err
		}
	}
	v, err := Version(ctx, binary)
	return VersionTag(v), err
}
func NewerRelease(current, candidate string) bool {
	parse := func(s string) ([3]int, bool) {
		var out [3]int
		s = strings.TrimPrefix(s, "v")
		parts := strings.Split(s, ".")
		if len(parts) != 3 {
			return out, false
		}
		for i, v := range parts {
			n, e := strconv.Atoi(v)
			if e != nil {
				return out, false
			}
			out[i] = n
		}
		return out, true
	}
	a, ok := parse(current)
	if !ok {
		return false
	}
	b, ok := parse(candidate)
	if !ok {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return b[i] > a[i]
		}
	}
	return false
}

func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return fmt.Errorf("订阅地址必须是有效 HTTP(S) URL，不支持 URL 用户认证")
	}
	return nil
}
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("响应超过 %d 字节限制", limit)
	}
	return b, nil
}
func safeNetworkError(err error) error {
	var u *url.Error
	if errors.As(err, &u) {
		return u.Err
	}
	return err
}
