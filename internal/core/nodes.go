package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	MaxNodesInputSize = 1 << 20
	AutoSelectGroup   = "auto-select"
	AllNodesGroup     = "all-nodes"
)

// 手动订阅规则只依赖内置匹配，私有地址直连，不触发 geo 数据库下载。
var nodeSubscriptionRules = []string{
	"DOMAIN,localhost,DIRECT",
	"DOMAIN-SUFFIX,local,DIRECT",
	"DOMAIN-SUFFIX,lan,DIRECT",
	"IP-CIDR,127.0.0.0/8,DIRECT,no-resolve",
	"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
	"IP-CIDR,172.16.0.0/12,DIRECT,no-resolve",
	"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
	"IP-CIDR,169.254.0.0/16,DIRECT,no-resolve",
	"IP-CIDR,100.64.0.0/10,DIRECT,no-resolve",
	"IP-CIDR,224.0.0.0/4,DIRECT,no-resolve",
	"IP-CIDR6,::1/128,DIRECT,no-resolve",
	"IP-CIDR6,fc00::/7,DIRECT,no-resolve",
	"IP-CIDR6,fe80::/10,DIRECT,no-resolve",
	"MATCH," + AutoSelectGroup,
}

// 节点名称与内置出口或生成的策略组相同时会被内核拒绝或产生歧义。
var reservedNodeNames = map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true, "PASS": true, "COMPATIBLE": true, "GLOBAL": true, AutoSelectGroup: true, AllNodesGroup: true}

type nodeGroup struct {
	Name      string   `yaml:"name"`
	Type      string   `yaml:"type"`
	Proxies   []string `yaml:"proxies"`
	URL       string   `yaml:"url,omitempty"`
	Interval  int      `yaml:"interval,omitempty"`
	Tolerance int      `yaml:"tolerance,omitempty"`
}

// 接受多行分享链接、节点 YAML/JSON 或其 Base64 编码；错误只报告位置，不回显凭据。
func ParseNodesInput(input string) ([]*yaml.Node, error) {
	if len(input) > MaxNodesInputSize {
		return nil, fmt.Errorf("手动节点不能超过 1 MiB")
	}
	raw := strings.TrimSpace(input)
	if raw == "" {
		return nil, fmt.Errorf("请填写节点分享链接或节点 YAML/JSON")
	}
	compact := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, raw)
	if decoded, err := decodeChainBase64(compact); err == nil && utf8.Valid(decoded) {
		raw = strings.TrimSpace(string(decoded))
	}
	var nodes []*yaml.Node
	var err error
	if first := firstNodeLine(raw); isShareLink(first) {
		nodes, err = parseShareLinks(raw)
	} else {
		nodes, err = parseNodeDocument(raw)
	}
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("未找到任何节点")
	}
	used := map[string]bool{}
	for i, n := range nodes {
		d := &Document{Root: yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{n}}}
		if err = normalizeProxyNode(d, fmt.Sprintf("第 %d 个节点", i+1)); err != nil {
			return nil, err
		}
		name := uniqueNodeName(d.Text("name"), used)
		used[name] = true
		put(n, "name", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name})
	}
	return nodes, nil
}

func firstNodeLine(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return ""
}

func parseShareLinks(raw string) ([]*yaml.Node, error) {
	var nodes []*yaml.Node
	for i, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !isShareLink(line) {
			return nil, fmt.Errorf("第 %d 行不是分享链接；分享链接与 YAML/JSON 不能混合输入", i+1)
		}
		proxy, err := parseChainURI(line)
		if err != nil {
			return nil, fmt.Errorf("第 %d 行：%w", i+1, err)
		}
		var n yaml.Node
		if err = n.Encode(proxy); err != nil {
			return nil, fmt.Errorf("第 %d 行分享链接无法转换为节点配置", i+1)
		}
		nodes = append(nodes, &n)
	}
	return nodes, nil
}

func parseNodeDocument(raw string) ([]*yaml.Node, error) {
	// 包装后复用项目的重复键、别名和单文档检查，兼容映射、列表和 proxies 段。
	parsed, err := Parse([]byte("node:\n  " + strings.ReplaceAll(raw, "\n", "\n  ")))
	var n *yaml.Node
	if err == nil {
		n = parsed.Get("node")
	} else if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
		// 以 Tab 缩进的 JSON 不是合法 YAML 块，改用 JSON 解析。
		if n, err = parseNodeJSON(raw); err != nil {
			return nil, fmt.Errorf("节点 JSON 格式无效")
		}
	} else {
		return nil, fmt.Errorf("节点 YAML 格式无效，请输入分享链接、节点映射或节点列表")
	}
	if n.Kind == yaml.MappingNode && lookup(n, "proxies") != nil {
		if len(n.Content) != 2 {
			return nil, fmt.Errorf("只能输入节点或 proxies 段，完整订阅请使用订阅 URL 来源")
		}
		n = lookup(n, "proxies")
	}
	switch n.Kind {
	case yaml.MappingNode:
		return []*yaml.Node{cloneNode(n)}, nil
	case yaml.SequenceNode:
		nodes := make([]*yaml.Node, 0, len(n.Content))
		for i, item := range n.Content {
			if item.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("第 %d 个节点必须是映射", i+1)
			}
			nodes = append(nodes, cloneNode(item))
		}
		return nodes, nil
	}
	return nil, fmt.Errorf("节点配置必须是映射或映射列表")
}

func parseNodeJSON(raw string) (*yaml.Node, error) {
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if d.More() {
		return nil, fmt.Errorf("JSON 只能包含一个值")
	}
	var n yaml.Node
	if err := n.Encode(jsonNumbers(v)); err != nil {
		return nil, err
	}
	return &n, nil
}

// json.Number 按字符串编码会把端口变成字符串，转换为整数或浮点数。
func jsonNumbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, item := range x {
			x[k] = jsonNumbers(item)
		}
	case []any:
		for i, item := range x {
			x[i] = jsonNumbers(item)
		}
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
	}
	return v
}

func uniqueNodeName(name string, used map[string]bool) string {
	if !used[name] && !reservedNodeNames[name] {
		return name
	}
	for i := 2; ; i++ {
		candidate := name + " (" + strconv.Itoa(i) + ")"
		if !used[candidate] && !reservedNodeNames[candidate] {
			return candidate
		}
	}
}

// JSON 输入的键值都带双引号；编码器会为需要保持字符串类型的值重新加引号。
func plainStrings(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!str" {
		n.Style &^= yaml.DoubleQuotedStyle | yaml.SingleQuotedStyle
	}
	for _, child := range n.Content {
		plainStrings(child)
	}
}

// 由手动节点生成固定模板订阅：自动测速组作为默认出口，全部节点组供手动选择。
func BuildNodeSubscription(input string) (*Document, error) {
	nodes, err := ParseNodesInput(input)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, lookup(n, "name").Value)
	}
	groups := []nodeGroup{
		{Name: AutoSelectGroup, Type: "url-test", Proxies: names, URL: DefaultDelayURL, Interval: 300, Tolerance: 50},
		{Name: AllNodesGroup, Type: "select", Proxies: append([]string{AutoSelectGroup}, names...)},
	}
	var groupNode, ruleNode yaml.Node
	if err = groupNode.Encode(groups); err != nil {
		return nil, err
	}
	if err = ruleNode.Encode(nodeSubscriptionRules); err != nil {
		return nil, err
	}
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	put(root, "proxies", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: nodes})
	put(root, "proxy-groups", &groupNode)
	put(root, "rules", &ruleNode)
	d := &Document{Root: yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}}
	formatSettingsNode(&d.Root)
	plainStrings(&d.Root)
	raw, err := d.Bytes()
	if err != nil {
		return nil, err
	}
	// 以序列化结果重新解析，确保缓存与运行配置读取的是同一份结构。
	if d, err = Parse(bytes.TrimSpace(raw)); err != nil {
		return nil, fmt.Errorf("生成节点订阅失败: %w", err)
	}
	return d, nil
}
