package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// 每次请求选择当前策略，避免启用保护后复用未打标记的旧连接。
type guardTransport struct{ manager *Manager }

func (t guardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m := t.manager
	spec, err := m.guard.discover(req.Context())
	if err != nil {
		return nil, err
	}
	if spec == nil {
		return http.DefaultTransport.RoundTrip(req)
	}
	if m.root != nil {
		reply, err := m.rootCall(req.Context(), rootRequest{Action: "http-get", URL: req.URL.String(), Method: req.Method, Header: req.Header})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: reply.StatusCode, Status: fmt.Sprintf("%d %s", reply.StatusCode, http.StatusText(reply.StatusCode)), Header: reply.Header, Body: io.NopCloser(bytes.NewReader(reply.Output)), ContentLength: int64(len(reply.Output)), Request: req}, nil
	}
	settings, err := m.Store.Settings()
	if err != nil {
		return nil, err
	}
	servers, err := guardDNSServers(settings)
	if err != nil {
		return nil, err
	}
	var next atomic.Uint32
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		dialer := net.Dialer{Timeout: 5 * time.Second, Control: guardSocketControl}
		server := servers[(next.Add(1)-1)%uint32(len(servers))]
		return dialer.DialContext(ctx, network, net.JoinHostPort(server, "53"))
	}}
	dialer := net.Dialer{Timeout: 20 * time.Second, Control: guardSocketControl, Resolver: resolver}
	transport := &http.Transport{Proxy: nil, DialContext: dialer.DialContext, DisableKeepAlives: true, ForceAttemptHTTP2: true}
	return transport.RoundTrip(req)
}

func guardDNSServers(settings Settings) ([]string, error) {
	values, err := StringList(settings.Config, "dns", "default-nameserver")
	if err != nil {
		return nil, err
	}
	var servers []string
	for _, value := range values {
		if ip := net.ParseIP(value); ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
			servers = append(servers, ip.String())
		}
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("断网保护下 tim 解析需要 dns.default-nameserver 至少包含一个非回环 IP 地址")
	}
	return servers, nil
}
