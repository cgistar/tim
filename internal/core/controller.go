package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Controller struct {
	Address, Secret string
	Client          *http.Client
}

type ControllerHTTPError struct {
	Path   string
	Status int
}

func (e *ControllerHTTPError) Error() string {
	return fmt.Sprintf("mihomo API %s: HTTP %d", e.Path, e.Status)
}

func NewController(d *Document) (*Controller, error) {
	addr, err := listenAddress(d.Text("external-controller"))
	if err != nil {
		return nil, err
	}
	return &Controller{Address: "http://" + addr, Secret: d.Text("secret"), Client: &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}}, nil
}
func (c *Controller) Do(ctx context.Context, method, path string, body, out any) error {
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Address+path, &b)
	if err != nil {
		return err
	}
	if c.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.Secret)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &ControllerHTTPError{Path: strings.SplitN(path, "?", 2)[0], Status: res.StatusCode}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
}

type ProxyInfo struct {
	Type string   `json:"type"`
	Now  string   `json:"now"`
	All  []string `json:"all"`
}
type RuntimeInfo struct {
	Mode string `json:"mode"`
	Tun  struct {
		Enable bool   `json:"enable"`
		Device string `json:"device"`
	} `json:"tun"`
}

func (c *Controller) Global(ctx context.Context) (ProxyInfo, error) {
	var p ProxyInfo
	err := c.Do(ctx, "GET", "/proxies/"+url.PathEscape("GLOBAL"), nil, &p)
	return p, err
}
func (c *Controller) Check(ctx context.Context, d *Document) (RuntimeInfo, string, error) {
	var info RuntimeInfo
	if err := c.Do(ctx, "GET", "/configs", nil, &info); err != nil {
		return info, "", err
	}
	if info.Mode != d.Text("mode") {
		return info, "", fmt.Errorf("实际模式 %s 与配置 %s 不符", info.Mode, d.Text("mode"))
	}
	tun := d.Text("tun", "enable") == "true"
	if info.Tun.Enable != tun {
		return info, "", fmt.Errorf("TUN 实际状态与配置不符")
	}
	if info.Mode != "global" {
		return info, "", nil
	}
	g, err := c.Global(ctx)
	if err != nil {
		return info, "", err
	}
	if len(g.All) == 0 || g.Now == "COMPATIBLE" || g.Now == "" {
		return info, g.Now, fmt.Errorf("GLOBAL 尚无实际代理节点")
	}
	if !contains(g.All, g.Now) {
		return info, g.Now, fmt.Errorf("GLOBAL 当前选择不在候选列表")
	}
	return info, g.Now, nil
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func redact(text, secret string) string {
	if secret != "" {
		text = strings.ReplaceAll(text, secret, "[已隐藏]")
	}
	return text
}
