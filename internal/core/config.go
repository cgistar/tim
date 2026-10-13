package core

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Document struct{ Root yaml.Node }

func Parse(data []byte) (*Document, error) { return parseDocument(data, false) }

// 用户覆盖可为空；订阅和管理配置仍要求有效映射。
func ParseUser(data []byte) (*Document, error) { return parseDocument(data, true) }

func parseDocument(data []byte, allowEmpty bool) (*Document, error) {
	if allowEmpty && len(bytes.TrimSpace(data)) == 0 {
		return NewUser()
	}
	var n yaml.Node
	d := yaml.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(&n); err != nil {
		if allowEmpty && err == io.EOF {
			user, e := NewUser()
			if e != nil {
				return nil, e
			}
			user.Root.Content[0].HeadComment = strings.TrimSpace(string(data))
			return user, nil
		}
		return nil, fmt.Errorf("解析 YAML: %w", err)
	}
	if len(n.Content) != 1 || n.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("配置必须是 YAML 映射")
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("配置只能包含一个 YAML 文档")
	}
	var check map[string]any
	if err := n.Decode(&check); err != nil {
		return nil, fmt.Errorf("配置字段: %w", err)
	}
	if hasAlias(n.Content[0]) {
		var expanded yaml.Node
		if err := expanded.Encode(check); err != nil {
			return nil, err
		}
		n.Content[0] = &expanded
	}
	if err := checkNode(n.Content[0]); err != nil {
		return nil, err
	}
	return &Document{Root: n}, nil
}
func hasAlias(n *yaml.Node) bool {
	if n.Kind == yaml.AliasNode {
		return true
	}
	for _, c := range n.Content {
		if hasAlias(c) {
			return true
		}
	}
	return false
}
func checkNode(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
		for i := 0; i < len(n.Content); i += 2 {
			if n.Content[i].Tag != "!!str" {
				return fmt.Errorf("配置键必须是字符串")
			}
		}
	}
	for _, c := range n.Content {
		if err := checkNode(c); err != nil {
			return err
		}
	}
	return nil
}
func (d *Document) Clone() *Document { return &Document{Root: *cloneNode(&d.Root)} }
func cloneNode(n *yaml.Node) *yaml.Node {
	c := *n
	c.Content = nil
	for _, v := range n.Content {
		c.Content = append(c.Content, cloneNode(v))
	}
	return &c
}
func (d *Document) Bytes() ([]byte, error) {
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	err := e.Encode(&d.Root)
	if err == nil {
		err = e.Close()
	}
	return b.Bytes(), err
}
func (d *Document) UserBytes() ([]byte, error) {
	root := d.Root.Content[0]
	if len(root.Content) == 0 {
		comment := strings.TrimSpace(root.HeadComment + "\n" + root.FootComment)
		if comment == "" {
			return []byte{}, nil
		}
		return []byte(comment + "\n"), nil
	}
	return d.Bytes()
}

func lookup(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
func put(n *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = v
			return
		}
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}
func (d *Document) Get(path ...string) *yaml.Node {
	n := d.Root.Content[0]
	for _, k := range path {
		n = lookup(n, k)
	}
	return n
}
func (d *Document) Text(path ...string) string {
	n := d.Get(path...)
	if n == nil {
		return ""
	}
	return n.Value
}
func (d *Document) Set(value any, path ...string) error {
	if len(path) == 0 {
		return fmt.Errorf("字段路径为空")
	}
	n := d.Root.Content[0]
	for _, k := range path[:len(path)-1] {
		v := lookup(n, k)
		if v == nil {
			v = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			put(n, k, v)
		}
		if v.Kind != yaml.MappingNode {
			return fmt.Errorf("%s 必须是映射", k)
		}
		n = v
	}
	var v yaml.Node
	if err := v.Encode(value); err != nil {
		return err
	}
	put(n, path[len(path)-1], &v)
	return nil
}

const DashboardURL = "https://github.com/Zephyruso/zashboard/releases/latest/download/dist-cdn-fonts.zip"

func NewUser() (*Document, error) {
	return Parse([]byte("{}\n"))
}

func (d *Document) Delete(path ...string) {
	if len(path) == 0 {
		return
	}
	var remove func(*yaml.Node, []string)
	remove = func(n *yaml.Node, keys []string) {
		if n == nil || n.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i < len(n.Content); i += 2 {
			if n.Content[i].Value != keys[0] {
				continue
			}
			if len(keys) > 1 {
				remove(n.Content[i+1], keys[1:])
				if n.Content[i+1].Kind != yaml.MappingNode || len(n.Content[i+1].Content) != 0 {
					return
				}
			}
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return
		}
	}
	remove(d.Root.Content[0], path)
}
func merge(dst, src *yaml.Node) {
	for i := 0; i < len(src.Content); i += 2 {
		k, v := src.Content[i].Value, src.Content[i+1]
		old := lookup(dst, k)
		if old != nil && old.Kind == yaml.MappingNode && v.Kind == yaml.MappingNode {
			merge(old, v)
		} else {
			put(dst, k, cloneNode(v))
		}
	}
}
func rules(n *yaml.Node) ([]string, error) {
	if n == nil {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("rules 必须是字符串列表")
	}
	var out []string
	for _, v := range n.Content {
		if v.Tag != "!!str" {
			return nil, fmt.Errorf("rules 项必须是字符串")
		}
		out = append(out, v.Value)
	}
	return out, nil
}

// Build 只注入 GLOBAL 的确定性候选顺序，选择记忆完全由 mihomo 负责。
func Build(base, user *Document) (*Document, []string, error) {
	out := base.Clone()
	br, err := rules(base.Get("rules"))
	if err != nil {
		return nil, nil, err
	}
	ur, err := rules(user.Get("rules"))
	if err != nil {
		return nil, nil, err
	}
	merge(out.Root.Content[0], user.Root.Content[0])
	merged := []string{}
	seen := map[string]bool{}
	warnings := []string{}
	for _, r := range ur {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		merged = append(merged, r)
		if strings.HasPrefix(strings.ToUpper(r), "MATCH,") {
			warnings = append(warnings, "用户 MATCH 规则会遮蔽后续订阅规则")
		}
	}
	merged = append(merged, br...)
	if err = out.Set(merged, "rules"); err != nil {
		return nil, nil, err
	}
	if out.Get("profile", "store-selected") == nil {
		if err = out.Set(true, "profile", "store-selected"); err != nil {
			return nil, nil, err
		}
	}
	if out.Text("profile", "store-selected") != "true" {
		return nil, nil, fmt.Errorf("profile.store-selected 必须为 true，以使用内核原生选择记忆")
	}
	if out.Text("mode") == "" {
		_ = out.Set("rule", "mode")
	}
	if err = validateConfig(out); err != nil {
		return nil, nil, err
	}
	if err = prepareGlobal(out); err != nil {
		return nil, nil, err
	}
	return out, warnings, nil
}
func prepareGlobal(d *Document) error {
	candidates := []string{}
	seen := map[string]bool{}
	groups := []*yaml.Node{}
	add := func(s string) {
		if s != "" && s != "GLOBAL" && !seen[s] {
			seen[s] = true
			candidates = append(candidates, s)
		}
	}
	if p := d.Get("proxies"); p != nil {
		if p.Kind != yaml.SequenceNode {
			return fmt.Errorf("proxies 必须是列表")
		}
		for _, v := range p.Content {
			n := lookup(v, "name")
			t := lookup(v, "type")
			if n == nil || t == nil {
				return fmt.Errorf("代理缺少 name/type")
			}
			if t.Value != "direct" && t.Value != "reject" && t.Value != "pass" {
				add(n.Value)
			}
		}
	}
	realCount := len(candidates)
	providers := []string{}
	if p := d.Get("proxy-providers"); p != nil {
		if p.Kind != yaml.MappingNode {
			return fmt.Errorf("proxy-providers 必须是映射")
		}
		for i := 0; i < len(p.Content); i += 2 {
			providers = append(providers, p.Content[i].Value)
		}
	}
	if p := d.Get("proxy-groups"); p != nil {
		if p.Kind != yaml.SequenceNode {
			return fmt.Errorf("proxy-groups 必须是列表")
		}
		for _, g := range p.Content {
			n := lookup(g, "name")
			if n == nil {
				return fmt.Errorf("策略组缺少 name")
			}
			if n.Value != "GLOBAL" {
				groups = append(groups, g)
				if realCount > 0 {
					add(n.Value)
				}
			}
		}
	}
	if realCount == 0 && len(providers) == 0 {
		if d.Text("mode") == "global" {
			return fmt.Errorf("全局模式没有可用代理节点")
		}
		return nil
	}
	// provider-only 时不加入 DIRECT 或策略组，确保 provider 首个真实节点是默认出口。
	if realCount > 0 {
		add("DIRECT")
		add("REJECT")
	}
	global := map[string]any{"name": "GLOBAL", "type": "select"}
	if len(candidates) > 0 {
		global["proxies"] = candidates
	}
	if len(providers) > 0 {
		global["use"] = providers
	}
	var node yaml.Node
	if err := node.Encode(global); err != nil {
		return err
	}
	groups = append(groups, &node)
	put(d.Root.Content[0], "proxy-groups", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: groups})
	return nil
}
func validateConfig(d *Document) error {
	if m := d.Text("mode"); m != "rule" && m != "global" {
		return fmt.Errorf("mode 仅支持 rule/global")
	}
	for _, key := range []string{"mixed-port", "port", "socks-port", "redir-port", "tproxy-port"} {
		if n := d.Get(key); n != nil {
			v, err := strconv.Atoi(n.Value)
			if n.Tag != "!!int" || err != nil || v < 0 || v > 65535 {
				return fmt.Errorf("%s 必须为 0–65535 整数", key)
			}
		}
	}
	for _, path := range [][]string{{"external-controller"}, {"dns", "listen"}} {
		v := d.Text(path...)
		if v == "" {
			continue
		}
		if _, err := listenAddress(v); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(path, "."), err)
		}
	}
	if d.Text("external-controller") == "" {
		return fmt.Errorf("external-controller 不能为空")
	}
	return nil
}
func listenAddress(v string) (string, error) {
	host, p, err := net.SplitHostPort(v)
	if err != nil {
		return "", fmt.Errorf("需要 host:port 格式")
	}
	port, err := strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("端口必须为 1–65535")
	}
	if host != "" && host != "*" && net.ParseIP(host) == nil && host != "localhost" {
		return "", fmt.Errorf("监听地址必须是 IP 或 localhost")
	}
	if host == "" || host == "*" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	return net.JoinHostPort(host, p), nil
}
