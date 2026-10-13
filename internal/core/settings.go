package core

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Settings struct {
	URL        string `json:"subscription_url"`
	FakeIP     bool
	Config     *Document
	Chain      ChainSettings
	DelayURL   string
	MihomoPath string
	DataDir    string
}

func DefaultSettings() (Settings, error) {
	d, err := Parse([]byte(`mixed-port: 7890
port: 0
socks-port: 0
bind-address: 127.0.0.1
allow-lan: false
external-controller: 0.0.0.0:9090
secret: ""
external-ui: ui
mode: rule
tun:
  enable: false
  stack: system
  auto-route: true
  auto-detect-interface: true
  strict-route: true
  dns-hijack: ["any:53", "tcp://any:53"]
profile:
  store-selected: true
  store-fake-ip: false
sniffer:
  enable: true
  force-dns-mapping: true
  parse-pure-ip: true
  override-destination: true
  sniff:
    HTTP: {ports: [80, "8080-8880"], override-destination: true}
    TLS: {ports: [443, 8443]}
    QUIC: {ports: [443, 8443]}
dns:
  enable: true
  listen: 0.0.0.0:1053
  ipv6: false
  cache-algorithm: arc
  prefer-h3: false
  use-hosts: true
  use-system-hosts: true
  respect-rules: true
  enhanced-mode: fake-ip
  fake-ip-filter-mode: blacklist
  fake-ip-filter: ['*.lan', '+.local', '+.cn']
  default-nameserver: [223.5.5.5, 119.29.29.29]
  nameserver: ["https://1.1.1.1/dns-query"]
  proxy-server-nameserver: ["https://doh.pub/dns-query#DIRECT", "https://dns.alidns.com/dns-query#DIRECT"]
  direct-nameserver: [system, "https://dns.alidns.com/dns-query", "https://doh.pub/dns-query"]
hosts:
  dns.alidns.com: [223.5.5.5, 223.6.6.6]
  doh.pub: [1.12.12.12, 120.53.53.53]
  services.googleapis.cn: [services.googleapis.com]
  '+.mcdn.bilivideo.com': [0.0.0.0]
  '+.mcdn.bilivideo.cn': [0.0.0.0]
`))
	if err != nil {
		return Settings{}, err
	}
	if err = d.Set(DashboardURL, "external-ui-url"); err != nil {
		return Settings{}, err
	}
	return Settings{FakeIP: true, Config: d, MihomoPath: "data/mihomo", DataDir: "data"}, nil
}

func (s Settings) Clone() Settings {
	s.Config = s.Config.Clone()
	return s
}
func (s Settings) Equal(other Settings) bool {
	return s.DataDir == other.DataDir && s.MihomoPath == other.MihomoPath && s.URL == other.URL && s.FakeIP == other.FakeIP && s.Chain == other.Chain && s.DelayURL == other.DelayURL && equalNode(s.Config.Root.Content[0], other.Config.Root.Content[0])
}
func equalNode(a, b *yaml.Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Kind != b.Kind || a.Tag != b.Tag || a.Value != b.Value || len(a.Content) != len(b.Content) {
		return false
	}
	if a.Kind == yaml.MappingNode {
		for i := 0; i < len(a.Content); i += 2 {
			if !equalNode(a.Content[i+1], lookup(b, a.Content[i].Value)) {
				return false
			}
		}
	} else {
		for i := range a.Content {
			if !equalNode(a.Content[i], b.Content[i]) {
				return false
			}
		}
	}
	return true
}
func (s Settings) Bytes() ([]byte, error) {
	d, err := Parse([]byte("{}"))
	if err != nil {
		return nil, err
	}
	if err = d.Set(s.URL, "subscription_url"); err != nil {
		return nil, err
	}
	if err = d.Set(s.FakeIP, "fake-ip-enabled"); err != nil {
		return nil, err
	}
	if err = d.Set(s.Chain, "chain-proxy"); err != nil {
		return nil, err
	}
	if err = d.Set(s.DelayURL, "delay-test-url"); err != nil {
		return nil, err
	}
	if err = d.Set(s.DataDir, "data-dir"); err != nil {
		return nil, err
	}
	if err = d.Set(s.MihomoPath, "mihomo-path"); err != nil {
		return nil, err
	}
	if s.Config == nil {
		return nil, fmt.Errorf("setting.yaml 缺少 config")
	}
	put(d.Root.Content[0], "config", cloneNode(s.Config.Root.Content[0]))
	formatSettingsNode(&d.Root)
	return d.Bytes()
}

// 只展开管理文件的集合节点，保留标量引号和注释，不改变用户 YAML 的格式。
func formatSettingsNode(n *yaml.Node) {
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		n.Style &^= yaml.FlowStyle
	}
	for _, child := range n.Content {
		formatSettingsNode(child)
	}
}

func ParseSettings(raw []byte) (Settings, error) {
	d, err := Parse(raw)
	if err != nil {
		return Settings{}, err
	}
	for _, field := range []struct{ key, tag string }{{"subscription_url", "!!str"}, {"fake-ip-enabled", "!!bool"}, {"config", "!!map"}, {"mihomo-path", "!!str"}, {"data-dir", "!!str"}} {
		n := d.Get(field.key)
		if n == nil || n.Tag != field.tag {
			return Settings{}, fmt.Errorf("setting.yaml.%s 必须为 %s", field.key, field.tag)
		}
	}
	config := &Document{Root: yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{cloneNode(d.Get("config"))}}}
	s := Settings{DataDir: d.Text("data-dir"), MihomoPath: d.Text("mihomo-path"), URL: d.Text("subscription_url"), FakeIP: d.Text("fake-ip-enabled") == "true", Config: config}
	if n := d.Get("delay-test-url"); n != nil {
		if n.Tag != "!!str" {
			return Settings{}, fmt.Errorf("delay-test-url 必须为字符串")
		}
		s.DelayURL = strings.TrimSpace(n.Value)
	}
	if chain := d.Get("chain-proxy"); chain != nil {
		if chain.Kind != yaml.MappingNode {
			return Settings{}, fmt.Errorf("chain-proxy 必须为映射")
		}
		for _, field := range []struct{ key, tag string }{{"enabled", "!!bool"}, {"input", "!!str"}} {
			if n := lookup(chain, field.key); n != nil && n.Tag != field.tag {
				return Settings{}, fmt.Errorf("chain-proxy.%s 类型无效", field.key)
			}
		}
		if err := chain.Decode(&s.Chain); err != nil {
			return Settings{}, fmt.Errorf("chain-proxy 格式无效")
		}
	}
	return s, s.Validate()
}
func (s Settings) Validate() error {
	if strings.TrimSpace(s.DataDir) == "" || strings.ContainsRune(s.DataDir, 0) {
		return fmt.Errorf("data-dir 必须为非空有效路径")
	}
	if strings.TrimSpace(s.MihomoPath) == "" || strings.ContainsRune(s.MihomoPath, 0) {
		return fmt.Errorf("mihomo-path 必须为非空有效路径")
	}
	if s.DelayURL != "" {
		if err := validateURL(s.DelayURL); err != nil {
			return fmt.Errorf("延迟测试地址必须为有效 HTTP/HTTPS URL")
		}
	}
	if s.Config == nil {
		return fmt.Errorf("setting.yaml 缺少 config")
	}
	if s.URL != "" {
		if err := validateURL(s.URL); err != nil {
			return err
		}
	}
	if err := validateConfig(s.Config); err != nil {
		return err
	}
	for _, path := range [][]string{{"dns", "ipv6"}, {"dns", "enable"}, {"sniffer", "enable"}, {"tun", "enable"}, {"allow-lan"}} {
		n := s.Config.Get(path...)
		if n == nil || n.Tag != "!!bool" {
			return fmt.Errorf("%s 必须为布尔值", strings.Join(path, "."))
		}
	}
	for _, key := range DNSListKeys {
		list, err := StringList(s.Config, "dns", key)
		if err != nil {
			return err
		}
		if key != "direct-nameserver" && len(list) == 0 {
			return fmt.Errorf("%s 不能为空", key)
		}
	}
	_, err := StringList(s.Config, "dns", "fake-ip-filter")
	return err
}

var DNSListKeys = []string{"default-nameserver", "nameserver", "proxy-server-nameserver", "direct-nameserver"}

func StringList(d *Document, path ...string) ([]string, error) {
	n := d.Get(path...)
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s 必须为字符串列表", strings.Join(path, "."))
	}
	out := make([]string, 0, len(n.Content))
	for _, v := range n.Content {
		if v.Tag != "!!str" || strings.TrimSpace(v.Value) == "" {
			return nil, fmt.Errorf("%s 包含空条目或非字符串", strings.Join(path, "."))
		}
		out = append(out, v.Value)
	}
	return out, nil
}
func ParseCommaList(text string, required bool) ([]string, error) {
	if strings.TrimSpace(text) == "" {
		if required {
			return nil, fmt.Errorf("DNS 上游不能为空")
		}
		return []string{}, nil
	}
	values := strings.Split(text, ",")
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
		if values[i] == "" {
			return nil, fmt.Errorf("DNS 上游包含空条目")
		}
	}
	return values, nil
}

func (s Settings) Automatic() (*Document, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	d := s.Config.Clone()
	if err := d.Set("fake-ip", "dns", "enhanced-mode"); err != nil {
		return nil, err
	}
	if err := d.Set("blacklist", "dns", "fake-ip-filter-mode"); err != nil {
		return nil, err
	}
	if !s.FakeIP {
		if err := d.Set([]string{"+.*"}, "dns", "fake-ip-filter"); err != nil {
			return nil, err
		}
	}
	return d, nil
}
func BuildConfigured(base, user *Document, settings Settings) (*Document, []string, error) {
	auto, err := settings.Automatic()
	if err != nil {
		return nil, nil, err
	}
	b := base.Clone()
	// DNS 由管理配置完整定义，避免订阅中的策略、fallback 等隐式混入默认模板。
	b.Delete("dns")
	merge(b.Root.Content[0], auto.Root.Content[0])
	cfg, warnings, err := Build(b, user)
	if err != nil {
		return nil, nil, err
	}
	warnings = append(warnings, OverrideWarnings(auto, user)...)
	return cfg, append(warnings, applyChain(cfg, settings.Chain)...), nil
}

// 只提示不同于自动配置的自定义值，不把静态配置关系表述为刚执行的操作。
func OverrideWarnings(auto, user *Document) []string {
	warnings := []string{}
	var visit func(*yaml.Node, *yaml.Node, []string)
	visit = func(a, u *yaml.Node, path []string) {
		if a == nil || u == nil || equalNode(a, u) {
			return
		}
		if a.Kind == yaml.MappingNode && u.Kind == yaml.MappingNode {
			for i := 0; i < len(a.Content); i += 2 {
				next := append(append([]string{}, path...), a.Content[i].Value)
				visit(a.Content[i+1], lookup(u, a.Content[i].Value), next)
			}
		} else {
			key := strings.Join(path, ".")
			if key == "secret" {
				return
			}
			warnings = append(warnings, fmt.Sprintf("%s 使用 user.yaml 中的值：%s", key, displayNode(u)))
		}
	}
	visit(auto.Root.Content[0], user.Root.Content[0], nil)
	return warnings
}

func displayNode(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	values := make([]string, 0, len(n.Content))
	for _, child := range n.Content {
		values = append(values, displayNode(child))
	}
	return "[" + strings.Join(values, ", ") + "]"
}

func (s *Settings) ResetDNS() error {
	defaults, err := DefaultSettings()
	if err != nil {
		return err
	}
	put(s.Config.Root.Content[0], "dns", cloneNode(defaults.Config.Get("dns")))
	s.FakeIP = defaults.FakeIP
	return s.Config.Set(true, "sniffer", "enable")
}

func (s *Store) initializeSettings() error {
	if _, err := os.Stat(s.Path("setting.yaml")); err == nil {
		_, err = s.Settings()
		return err
	} else if !os.IsNotExist(err) {
		return err
	}
	settings, err := DefaultSettings()
	if err != nil {
		return err
	}
	if s.DataOverride != "" {
		settings.MihomoPath = s.ResolvePath(s.DataOverride + "/mihomo")
	}
	raw, err := settings.Bytes()
	if err != nil {
		return err
	}
	return AtomicWrite(s.Path("setting.yaml"), raw, 0600)
}
