package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const PublicIPURL = "https://api.country.is/"

type PublicIP struct {
	IP      string `json:"ip"`
	Country string `json:"country"`
}

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
func QueryPublicIP(ctx context.Context, client *http.Client, endpoint string) (PublicIP, error) {
	var result PublicIP
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return result, err
	}
	res, err := client.Do(req)
	if err != nil {
		return result, safeNetworkError(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return result, fmt.Errorf("IP 查询 HTTP %d", res.StatusCode)
	}
	b, err := readBounded(res.Body, 64<<10)
	if err != nil {
		return result, err
	}
	if err = json.Unmarshal(b, &result); err != nil {
		return result, err
	}
	if net.ParseIP(result.IP) == nil {
		return result, fmt.Errorf("IP 查询返回无效地址")
	}
	return result, nil
}
func (m *Manager) PublicIP(ctx context.Context) (PublicIP, error) {
	_, running, err := m.process()
	if err != nil {
		return PublicIP{}, err
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	if running {
		client, err = m.ProxyClient(ctx)
		if err != nil {
			return PublicIP{}, err
		}
	}
	defer client.CloseIdleConnections()
	return QueryPublicIP(ctx, client, PublicIPURL)
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
