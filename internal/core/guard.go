package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type GuardStatus struct {
	Enabled   bool
	Active    bool
	Backend   string
	Error     string
	NeedsRoot bool
}

// 后端信息从实际规则与本机工具发现，不保存开启意图。
type guardSpec struct {
	Backend  string            `json:"backend"`
	Programs map[string]string `json:"programs"`
}

type networkGuard struct {
	remote   *Manager
	lockPath string
	lookup   func(string) (string, error)
	run      func(context.Context, string, []byte, ...string) ([]byte, error)
}

func newNetworkGuard() *networkGuard {
	return &networkGuard{lockPath: "/run/lock/tim-network-guard.lock", lookup: exec.LookPath, run: func(ctx context.Context, program string, input []byte, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, program, args...)
		cmd.Stdin = strings.NewReader(string(input))
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return out, fmt.Errorf("%s %s 失败: %w\n%s", program, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return out, nil
	}}
}

// 查询所有可用后端，避免把无记录但仍存在的规则误报为关闭。
func (g *networkGuard) discover(ctx context.Context) (*guardSpec, error) {
	if g.remote != nil {
		for _, name := range []string{"nft", "iptables", "ip6tables"} {
			if _, err := g.lookup(name); err == nil {
				reply, err := g.remote.rootCall(ctx, rootRequest{Action: "guard-discover"})
				return reply.Spec, err
			}
		}
		return nil, nil
	}
	var found *guardSpec
	var failures []error
	for _, backend := range []string{"nftables", "iptables"} {
		spec := &guardSpec{Backend: backend, Programs: map[string]string{}}
		for _, name := range guardProgramNames(backend) {
			path, err := g.lookup(name)
			if err == nil {
				spec.Programs[name] = path
			}
		}
		if backend == "nftables" && spec.Programs["nft"] == "" {
			continue
		}
		if backend == "iptables" {
			if spec.Programs["iptables"] == "" && spec.Programs["ip6tables"] == "" {
				continue
			}
			if spec.Programs["iptables"] == "" || spec.Programs["ip6tables"] == "" {
				failures = append(failures, fmt.Errorf("缺少双栈 iptables 查询工具"))
				continue
			}
		}
		exists, err := g.exists(ctx, spec)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if exists {
			if found != nil {
				return nil, fmt.Errorf("多个后端存在 tim 保护规则，请先明确清理冲突")
			}
			found = spec
		}
	}
	if len(failures) != 0 {
		return found, errors.Join(failures...)
	}
	return found, nil
}

func guardProgramNames(backend string) []string {
	switch backend {
	case "nftables":
		return []string{"nft"}
	case "iptables":
		return []string{"iptables", "ip6tables", "iptables-restore", "ip6tables-restore"}
	default:
		return nil
	}
}

func (g *networkGuard) command(ctx context.Context, spec *guardSpec, name string, input []byte, args ...string) ([]byte, error) {
	program := spec.Programs[name]
	if program == "" {
		return nil, fmt.Errorf("缺少防火墙工具 %s", name)
	}
	out, err := g.run(ctx, program, input, args...)
	// nft/iptables 通过 stderr 报告内核权限错误，退出码本身不携带 EPERM。
	if err != nil && (strings.Contains(string(out), "Operation not permitted") || strings.Contains(string(out), "Permission denied") || strings.Contains(string(out), "you must be root")) {
		err = errors.Join(err, os.ErrPermission)
	}
	return out, err
}

func (m *Manager) guardConfig(cfg *Document) error {
	spec, err := m.guard.discover(context.Background())
	if err != nil || spec == nil {
		return err
	}
	return configureGuard(cfg)
}

func configureGuard(cfg *Document) error {
	if cfg.Text("tun", "auto-redirect") == "true" {
		return fmt.Errorf("断网保护与 tun.auto-redirect 的标记模式不兼容，请先关闭 auto-redirect")
	}
	// 局部标记会覆盖全局标记；拒绝冲突，不能默默改写用户节点。
	var walk func(*yaml.Node) error
	walk = func(node *yaml.Node) error {
		if node.Kind == yaml.MappingNode {
			for i := 0; i < len(node.Content); i += 2 {
				mark := node.Content[i+1].Value
				if node.Content[i].Value == "routing-mark" && mark != "0" && mark != fmt.Sprint(guardMihomoMark) {
					return fmt.Errorf("断网保护与自定义 routing-mark 冲突")
				}
			}
		}
		for _, child := range node.Content {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(&cfg.Root); err != nil {
		return err
	}
	if err := cfg.Set(guardMihomoMark, "routing-mark"); err != nil {
		return err
	}
	return cfg.Set(guardDevice, "tun", "device")
}

func (m *Manager) buildConfigured(base, user *Document, settings Settings) (*Document, []string, error) {
	cfg, warnings, err := BuildConfigured(base, user, settings)
	if err == nil {
		err = m.guardConfig(cfg)
	}
	return cfg, warnings, err
}

func (m *Manager) NetworkGuardStatus(ctx context.Context) (GuardStatus, error) {
	spec, err := m.guard.discover(ctx)
	if err != nil {
		return GuardStatus{Error: err.Error(), NeedsRoot: errors.Is(err, os.ErrPermission)}, err
	}
	if spec == nil {
		return GuardStatus{}, nil
	}
	st, err := m.guard.status(ctx, spec)
	st.NeedsRoot = errors.Is(err, os.ErrPermission)
	return st, err
}

func (g *networkGuard) status(ctx context.Context, spec *guardSpec) (GuardStatus, error) {
	if g.remote != nil {
		reply, err := g.remote.rootCall(ctx, rootRequest{Action: "guard-status"})
		return reply.Guard, err
	}
	st := GuardStatus{Backend: spec.Backend}
	exists, err := g.exists(ctx, spec)
	if err != nil {
		st.Error = err.Error()
		return st, err
	}
	if !exists {
		return st, nil
	}
	st.Enabled = true
	err = g.verify(ctx, spec)
	if err != nil {
		st.Error = err.Error()
		return st, err
	}
	st.Active = true
	return st, nil
}

// 仅显式开启时修改规则；后续配置失败也不自动解除已经生效的保护。
func (m *Manager) EnableNetworkGuard(ctx context.Context) error {
	if err := m.guardPrivileges(ctx); err != nil {
		return err
	}
	return m.withOperation(func() error {
		unlock, err := m.guardLock()
		if err != nil {
			return err
		}
		defer unlock()
		spec, err := m.guard.discover(ctx)
		if err != nil {
			return err
		}
		if spec == nil {
			spec, err = m.guard.selectBackend(ctx)
			if err != nil {
				return err
			}
		}
		settings, err := m.Store.Settings()
		if err != nil {
			return err
		}
		if _, err = guardDNSServers(settings); err != nil {
			return err
		}
		if cfg, _, previewErr := m.Store.Preview(); previewErr == nil {
			if err = configureGuard(cfg); err != nil {
				return err
			}
		} else if !errors.Is(previewErr, os.ErrNotExist) {
			return previewErr
		}
		if err = m.guard.apply(ctx, spec); err != nil {
			return fmt.Errorf("保护规则未完整生效，请重试或明确关闭: %w", err)
		}
		_, live, err := m.managedProcess(ctx)
		if err != nil {
			return err
		}
		if live {
			// 必须重建旧 socket，让 mihomo 真正使用 SO_MARK。
			if err = m.applySettings(ctx, settings); err != nil {
				return fmt.Errorf("保护规则已应用，标记内核运行配置失败，请重试或明确关闭保护: %w", err)
			}
		} else if raw, readErr := os.ReadFile(m.Store.Path("config.yaml")); readErr == nil {
			cfg, err := Parse(raw)
			if err != nil {
				return err
			}
			if err = configureGuard(cfg); err != nil {
				return err
			}
			binary, err := m.Store.Kernel()
			if err != nil {
				return err
			}
			if err = m.validate(ctx, binary, cfg); err != nil {
				return err
			}
			raw, err = cfg.Bytes()
			if err != nil {
				return err
			}
			if err = AtomicWrite(m.Store.Path("config.yaml"), raw, 0600); err != nil {
				return err
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		return nil
	})
}

// 调用方必须先获得“关闭保护并恢复直连”的明确确认。
func (m *Manager) DisableNetworkGuard(ctx context.Context) error {
	if err := m.guardPrivileges(ctx); err != nil {
		return err
	}
	return m.disableNetworkGuard(ctx)
}

func (m *Manager) disableNetworkGuard(ctx context.Context) error {
	if m.root != nil {
		_, err := m.rootCall(ctx, rootRequest{Action: "guard-disable"})
		return err
	}
	unlock, err := m.guardLock()
	if err != nil {
		return err
	}
	defer unlock()
	spec, err := m.guard.discover(ctx)
	if err != nil || spec == nil {
		return err
	}
	if err = m.guard.remove(ctx, spec); err != nil {
		return errors.Join(err, m.guard.apply(context.WithoutCancel(ctx), spec))
	}
	return nil
}

func NetworkGuardState(ctx context.Context) (GuardStatus, error) {
	m := &Manager{guard: newNetworkGuard()}
	return m.NetworkGuardStatus(ctx)
}

// 紧急恢复入口不依赖 DATA、订阅、内核文件或 setting.yaml 完整性。
func DisableGlobalNetworkGuard(ctx context.Context) error {
	m := &Manager{guard: newNetworkGuard()}
	return m.DisableNetworkGuard(ctx)
}

func (m *Manager) guardPrivileges(ctx context.Context) error {
	if m.root != nil {
		_, err := m.rootCall(ctx, rootRequest{Action: "guard-discover"})
		return err
	}
	return guardPrivileges()
}
func (m *Manager) guardLock() (func(), error) {
	// 助手在应用规则时持有主机锁，普通用户不打开 root 锁文件。
	if m.root != nil {
		return func() {}, nil
	}
	return lockFile(m.guard.lockPath)
}

// 只读展示不触发 sudo；已有会话授权时复用助手。
func (m *Manager) readNetworkGuardStatus(ctx context.Context) (GuardStatus, error) {
	if m.root == nil || m.root.authorized() {
		return m.NetworkGuardStatus(ctx)
	}
	local := *m.guard
	local.remote = nil
	reader := &Manager{guard: &local}
	return reader.NetworkGuardStatus(ctx)
}
