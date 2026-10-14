package core

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/metacubex/mihomo/common/convert"
	"gopkg.in/yaml.v3"
)

const (
	chainMetadata     = "x-tim-chain"
	chainProxyPrefix  = "__tim_front__"
	MaxChainInputSize = 64 << 10
)

type ChainSettings struct {
	Enabled bool   `yaml:"enabled"`
	Input   string `yaml:"input"`
}

// 运行信息随 config.yaml 提交，避免仅保存的设置被显示为已生效。
type ChainRuntime struct {
	Name   string `yaml:"name,omitempty"`
	Proxy  string `yaml:"proxy,omitempty"`
	Reason string `yaml:"reason,omitempty"`
}

func (r ChainRuntime) Text() string {
	if r.Reason != "" {
		return "未生效（" + r.Reason + "）"
	}
	return r.Name
}

func ReadChainRuntime(d *Document) ChainRuntime {
	return ChainRuntime{Name: d.Text(chainMetadata, "name"), Proxy: d.Text(chainMetadata, "proxy"), Reason: d.Text(chainMetadata, "reason")}
}

func setChainRuntime(d *Document, r ChainRuntime) {
	var n yaml.Node
	_ = n.Encode(r)
	put(d.Root.Content[0], chainMetadata, &n)
}

// 接受一个独立节点，分享链接的协议细节复用 mihomo 转换器。
func ParseChainInput(input string) (*Document, error) {
	if len(input) > MaxChainInputSize {
		return nil, fmt.Errorf("前置配置不能超过 64 KiB")
	}
	raw := strings.TrimSpace(input)
	if raw == "" {
		return nil, fmt.Errorf("请填写前置节点配置或分享链接")
	}
	compact := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, raw)
	if decoded, err := decodeChainBase64(compact); err == nil {
		raw = strings.TrimSpace(string(decoded))
	}
	var d *Document
	if scheme, _, ok := strings.Cut(raw, "://"); ok && !strings.ContainsAny(scheme, " \t\r\n:{}") {
		if strings.ContainsAny(raw, "\r\n") {
			return nil, fmt.Errorf("只能输入一个前置节点，不能导入订阅列表")
		}
		proxy, err := parseChainURI(raw)
		if err != nil {
			return nil, err
		}
		var n yaml.Node
		if err = n.Encode(proxy); err != nil {
			return nil, fmt.Errorf("分享链接无法转换为节点配置")
		}
		d = &Document{Root: yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{&n}}}
	} else {
		// 包装后仍复用项目的重复键、别名和单文档检查，同时兼容复制出的单项列表。
		wrapped := "node:\n  " + strings.ReplaceAll(raw, "\n", "\n  ")
		parsed, err := Parse([]byte(wrapped))
		if err != nil {
			return nil, fmt.Errorf("前置 YAML 格式无效，请输入单个节点配置")
		}
		n := parsed.Get("node")
		if n.Kind == yaml.MappingNode && lookup(n, "proxies") != nil {
			if len(n.Content) != 2 {
				return nil, fmt.Errorf("仅支持单个前置节点，不能导入完整规则或策略组配置")
			}
			n = lookup(n, "proxies")
		}
		if n.Kind == yaml.SequenceNode {
			if len(n.Content) != 1 {
				return nil, fmt.Errorf("前置配置必须恰好包含一个节点")
			}
			n = n.Content[0]
		}
		if n.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("前置配置必须是一个节点映射")
		}
		d = &Document{Root: yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{cloneNode(n)}}}
	}
	for _, key := range []string{"proxies", "proxy-groups", "proxy-providers", "rules", "dns"} {
		if d.Get(key) != nil {
			return nil, fmt.Errorf("前置配置只能包含单个节点，不能包含 %s", key)
		}
	}
	if n := d.Get("dialer-proxy"); n != nil {
		if n.Tag != "!!str" || n.Value != "" {
			return nil, fmt.Errorf("独立前置节点不能设置 dialer-proxy")
		}
		d.Delete("dialer-proxy")
	}
	kind := d.Text("type")
	if n := d.Get("type"); n == nil || n.Tag != "!!str" || kind == "" {
		return nil, fmt.Errorf("前置节点缺少 type")
	}
	switch strings.ToLower(kind) {
	case "direct", "reject", "reject-drop", "pass", "dns", "select", "url-test", "fallback", "load-balance", "relay":
		return nil, fmt.Errorf("前置必须是独立代理节点，不能是策略组或内置出口")
	}
	if n := d.Get("name"); n != nil && n.Tag != "!!str" {
		return nil, fmt.Errorf("前置节点 name 必须为字符串")
	}
	if strings.TrimSpace(d.Text("name")) == "" {
		name := kind
		if server := d.Text("server"); server != "" {
			name += " · " + server
		}
		_ = d.Set(name, "name")
	}
	if strings.IndexFunc(d.Text("name"), unicode.IsControl) >= 0 {
		return nil, fmt.Errorf("前置节点名称不能包含控制字符")
	}
	return d, nil
}

func decodeChainBase64(s string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := encoding.DecodeString(s); err == nil {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("Base64 编码无效")
}

func parseChainURI(raw string) (map[string]any, error) {
	scheme, body, _ := strings.Cut(raw, "://")
	scheme = strings.ToLower(scheme)
	switch scheme {
	case "vless", "vmess", "ss", "ssr", "trojan", "hysteria", "hysteria2", "hy2", "tuic", "anytls", "socks", "socks5", "socks5h", "http", "https":
	default:
		return nil, fmt.Errorf("暂不支持此协议的分享链接，请改用单节点 YAML")
	}
	raw = scheme + "://" + body
	// 上游会将部分 VLESS/VMess 错误连同完整 URI 写日志；在边界拒绝这些输入，避免泄漏凭据。
	if scheme == "vless" || (scheme == "vmess" && strings.Contains(body, "@")) {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || u.Port() == "" || u.User == nil || u.User.Username() == "" {
			return nil, fmt.Errorf("分享链接缺少有效的身份、服务器或端口")
		}
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return nil, fmt.Errorf("分享链接参数编码无效")
		}
		if ed := q.Get("ed"); ed != "" {
			if _, err := strconv.Atoi(ed); err != nil {
				return nil, fmt.Errorf("分享链接 ed 必须为整数")
			}
		}
	}
	if scheme == "vmess" && !strings.Contains(body, "@") {
		decoded, err := decodeChainBase64(body)
		if err != nil {
			return nil, fmt.Errorf("VMess 链接的 Base64 内容无效")
		}
		raw = "vmess://" + base64.StdEncoding.EncodeToString(decoded)
	}
	proxies, err := convert.ConvertsV2Ray([]byte(raw))
	if err != nil || len(proxies) != 1 {
		return nil, fmt.Errorf("分享链接无效，无法解析为单个代理节点")
	}
	proxy := proxies[0]
	if scheme == "socks" || scheme == "socks5" || scheme == "socks5h" || scheme == "http" || scheme == "https" {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("代理链接格式无效")
		}
		// 上游版本未正确处理百分号编码和密码中的冒号，按 URL userinfo 语义补全。
		username, password := "", ""
		if u.User != nil {
			username = u.User.Username()
			var hasPassword bool
			password, hasPassword = u.User.Password()
			if !hasPassword {
				if decoded, err := decodeChainBase64(username); err == nil {
					if user, pass, paired := strings.Cut(string(decoded), ":"); paired {
						username, password = user, pass
					}
				}
			}
		}
		proxy["username"], proxy["password"] = username, password
		proxy["skip-cert-verify"] = u.Query().Get("skip-cert-verify") == "true" || u.Query().Get("insecure") == "1"
	}
	port, err := strconv.Atoi(fmt.Sprint(proxy["port"]))
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("分享链接端口必须为 1–65535")
	}
	proxy["port"] = port
	if server, ok := proxy["server"].(string); !ok || strings.TrimSpace(server) == "" {
		return nil, fmt.Errorf("分享链接缺少服务器地址")
	}
	return proxy, nil
}

func applyChain(d *Document, settings ChainSettings) []string {
	d.Delete(chainMetadata)
	if !settings.Enabled {
		return nil
	}
	front, err := ParseChainInput(settings.Input)
	if err != nil {
		setChainRuntime(d, ChainRuntime{Reason: err.Error()})
		return []string{"链式前置未生效：" + err.Error()}
	}
	name := chainProxyPrefix
	for i := 1; chainNameUsed(d, name); i++ {
		name = chainProxyPrefix + strconv.Itoa(i)
	}
	label := front.Text("name")
	_ = front.Set(name, "name")
	count := 0
	if proxies := d.Get("proxies"); proxies != nil {
		for _, proxy := range proxies.Content {
			kind := lookup(proxy, "type")
			if kind == nil {
				continue
			}
			switch kind.Value {
			case "direct", "reject", "reject-drop", "pass", "dns":
				continue
			}
			put(proxy, "dialer-proxy", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name})
			count++
		}
	}
	if providers := d.Get("proxy-providers"); providers != nil {
		for i := 1; i < len(providers.Content); i += 2 {
			provider := providers.Content[i]
			if provider.Kind != yaml.MappingNode {
				continue
			}
			override := lookup(provider, "override")
			if override == nil {
				override = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				put(provider, "override", override)
			}
			if override.Kind != yaml.MappingNode {
				continue
			}
			put(override, "dialer-proxy", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name})
			count++
		}
	}
	if count == 0 {
		setChainRuntime(d, ChainRuntime{Reason: "没有可用代理出口"})
		return []string{"链式前置未生效：没有可用代理出口"}
	}
	// 自动收录节点的组需排除此独立前置，避免成为普通出口候选。
	if groups := d.Get("proxy-groups"); groups != nil {
		for _, group := range groups.Content {
			all, allProxies := lookup(group, "include-all"), lookup(group, "include-all-proxies")
			if (all == nil || all.Value != "true") && (allProxies == nil || allProxies.Value != "true") {
				continue
			}
			pattern := "^" + regexp.QuoteMeta(name) + "$"
			if old := lookup(group, "exclude-filter"); old != nil && old.Value != "" {
				pattern = "(?:" + old.Value + ")|" + pattern
			}
			put(group, "exclude-filter", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: pattern})
		}
	}
	proxies := d.Get("proxies")
	if proxies == nil {
		proxies = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		put(d.Root.Content[0], "proxies", proxies)
	}
	proxies.Content = append(proxies.Content, cloneNode(front.Root.Content[0]))
	setChainRuntime(d, ChainRuntime{Name: label, Proxy: name})
	return nil
}

func chainNameUsed(d *Document, name string) bool {
	var used func(*yaml.Node) bool
	used = func(n *yaml.Node) bool {
		if n.Kind == yaml.ScalarNode && n.Value == name {
			return true
		}
		for _, child := range n.Content {
			if used(child) {
				return true
			}
		}
		return false
	}
	return used(&d.Root)
}

func (m *Manager) validateWithChain(ctx context.Context, binary string, cfg, base, user *Document, settings Settings) (*Document, error) {
	err := m.validate(ctx, binary, cfg)
	if err == nil {
		return cfg, nil
	}
	var exitError *exec.ExitError
	if ReadChainRuntime(cfg).Proxy == "" || !errors.As(err, &exitError) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	// 只有内核拒绝含前置的配置、且原配置独立通过时，才停用这项附加功能。
	plainSettings := settings.Clone()
	plainSettings.Chain.Enabled = false
	plain, _, buildErr := BuildConfigured(base, user, plainSettings)
	if buildErr != nil {
		return nil, buildErr
	}
	if plainErr := m.validate(ctx, binary, plain); plainErr != nil {
		return nil, plainErr
	}
	setChainRuntime(plain, ChainRuntime{Reason: "前置配置未通过内核校验"})
	return plain, nil
}
