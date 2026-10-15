package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"gopkg.in/yaml.v3"
	"os"
	"time"
)

func (m *Manager) UpdateSubscription(ctx context.Context, hot bool) error {
	return m.withOperation(func() error {
		if hot {
			if _, live, err := m.processContext(ctx); err != nil {
				return err
			} else if !live {
				return fmt.Errorf("热更新要求 mihomo 正在运行，未下载订阅")
			}
		}
		s, err := m.Store.Settings()
		if err != nil {
			return err
		}
		return m.refreshSubscription(ctx, s, hot)
	})
}
func (m *Manager) applyHot(ctx context.Context, base, user *Document, settings Settings) error {
	p, live, err := m.processContext(ctx)
	if err != nil {
		return err
	}
	if !live {
		return fmt.Errorf("热更新期间 mihomo 已停止")
	}
	oldRaw, err := os.ReadFile(m.Store.Path("config.yaml"))
	if err != nil {
		return err
	}
	old, err := Parse(oldRaw)
	if err != nil {
		return err
	}
	cfg, _, err := m.buildConfigured(base, user, settings)
	if err != nil {
		return err
	}
	cfg, err = m.validateWithChain(ctx, p.Binary, cfg, base, user, settings)
	if err != nil {
		return err
	}
	// 保持控制通道和监听入口稳定，热更新不以断开管理连接为代价。
	for _, path := range [][]string{{"external-controller"}, {"secret"}, {"mixed-port"}, {"port"}, {"socks-port"}, {"bind-address"}, {"tun", "enable"}, {"dns", "listen"}} {
		if cfg.Text(path...) != old.Text(path...) {
			return fmt.Errorf("热更新不允许更改 %v，请使用普通更新", path)
		}
	}
	raw, err := cfg.Bytes()
	if err != nil {
		return err
	}
	controller, err := NewController(old)
	if err != nil {
		return err
	}
	check := func(ctx context.Context, d *Document) error {
		same, err := m.matchesManaged(ctx, p)
		if err != nil {
			return err
		}
		if !same {
			return fmt.Errorf("热更新期间内核进程已变化")
		}
		info, _, err := controller.Check(ctx, d)
		if err != nil {
			return err
		}
		return m.verifyProcess(ctx, p, d, info)
	}

	baseRaw, err := base.Bytes()
	if err != nil {
		return err
	}
	settingsRaw, err := settings.Bytes()
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = commitFiles(m.Store, []fileWrite{{"subscription.yaml", baseRaw}, {"config.yaml", raw}, {"setting.yaml", settingsRaw}}); err != nil {
		return err
	}
	if err = controller.Do(ctx, "PUT", "/configs?force=true", map[string]string{"payload": string(raw)}, nil); err != nil {
		return fmt.Errorf("配置已保存，但热更新失败: %w", err)
	}
	if err = check(ctx, cfg); err != nil {
		return fmt.Errorf("配置已保存，但热更新运行检查失败: %w", err)
	}
	return nil
}

func (m *Manager) SaveSettings(v Settings) error {
	return m.withOperation(func() error {
		if v.URL != "" {
			if err := validateURL(v.URL); err != nil {
				return err
			}
		}
		current, err := m.Store.Settings()
		if err != nil {
			return err
		}
		current.URL = v.URL
		raw, err := current.Bytes()
		if err != nil {
			return err
		}
		return AtomicWrite(m.Store.Path("setting.yaml"), raw, 0600)
	})
}
func (m *Manager) Refresh(ctx context.Context) error {
	return m.withOperation(func() error {
		s, err := m.Store.Settings()
		if err != nil {
			return err
		}
		return m.refresh(ctx, s)
	})
}

// 地址、下载缓存与运行配置分别提交，后续失败不撤销已完成的阶段。
func (m *Manager) RefreshURL(ctx context.Context, rawURL string) error {
	return m.withOperation(func() error {
		if err := validateURL(rawURL); err != nil {
			return err
		}
		s, err := m.Store.Settings()
		if err != nil {
			return err
		}
		s.URL = rawURL
		raw, err := s.Bytes()
		if err != nil {
			return err
		}
		if err = AtomicWrite(m.Store.Path("setting.yaml"), raw, 0600); err != nil {
			return fmt.Errorf("保存订阅地址失败: %w", err)
		}
		if err = m.refresh(ctx, s); err != nil {
			return fmt.Errorf("订阅地址已保存；%w", err)
		}
		return nil
	})
}

func (m *Manager) refresh(ctx context.Context, s Settings) error {
	return m.refreshSubscription(ctx, s, false)
}
func (m *Manager) refreshSubscription(ctx context.Context, s Settings, hot bool) error {
	base, err := m.downloadSubscription(ctx, s.URL)
	if err != nil {
		return err
	}
	// 缓存只依赖下载与 YAML 解析，不依赖用户覆盖、内核安装或启动结果。
	raw, err := base.Bytes()
	if err != nil {
		return err
	}
	if err = AtomicWrite(m.Store.Path("subscription.yaml"), raw, 0600); err != nil {
		return fmt.Errorf("订阅已下载，但保存缓存失败: %w", err)
	}
	if err = m.applySubscription(ctx, base, s, hot); err != nil {
		return fmt.Errorf("订阅下载成功并已保存；内核应用失败: %w", err)
	}
	return nil
}

func (m *Manager) downloadSubscription(ctx context.Context, rawURL string) (*Document, error) {
	if err := validateURL(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tim/1.0")
	started := time.Now()
	res, err := m.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("订阅下载超时（已等待 %s，原缓存保留）：订阅服务或上游未在期限内完成响应: %w", time.Since(started).Round(time.Second), safeNetworkError(err))
		}
		return nil, fmt.Errorf("订阅下载失败（原缓存保留）: %w", safeNetworkError(err))
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("订阅返回 HTTP %d，原缓存保留", res.StatusCode)
	}
	b, err := readBounded(res.Body, 8<<20)
	if err != nil {
		return nil, fmt.Errorf("读取订阅失败（原缓存保留）: %w", err)
	}
	base, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("订阅解析失败（原缓存保留）: %w", err)
	}
	return base, nil
}

func (m *Manager) applySubscription(ctx context.Context, base *Document, s Settings, hot bool) error {
	u, err := m.Store.User()
	if err != nil {
		return err
	}
	if hot {
		return m.applyHot(ctx, base, u, s)
	}
	return m.apply(ctx, base, u, s)
}

func (s *Store) Base() (*Document, error) {
	b, err := os.ReadFile(s.Path("subscription.yaml"))
	if err != nil {
		return nil, fmt.Errorf("请先下载有效订阅: %w", err)
	}
	return Parse(b)
}
func (s *Store) Preview() (*Document, []string, error) {
	base, err := s.Base()
	if err != nil {
		return nil, nil, err
	}
	u, err := s.User()
	if err != nil {
		return nil, nil, err
	}
	settings, err := s.Settings()
	if err != nil {
		return nil, nil, err
	}
	return BuildConfigured(base, u, settings)
}

func (m *Manager) Candidate() (*Document, error) { return m.Store.Base() }
func (m *Manager) Preview() (*Document, []string, error) {
	cfg, warnings, err := m.Store.Preview()
	if err != nil {
		return cfg, warnings, err
	}
	guard, err := m.readNetworkGuardStatus(context.Background())
	if err != nil {
		warnings = append(warnings, "无法读取断网保护状态，预览未包含保护参数: "+err.Error())
		return cfg, warnings, nil
	}
	if guard.Active {
		err = configureGuard(cfg)
	}
	return cfg, warnings, err
}

// 模式和 TUN 仅由管理设置决定，过滤副本以保留用户原文件。
func userOverrides(user *Document) *Document {
	overrides := user.Clone()
	overrides.Delete("mode")
	overrides.Delete("tun")
	return overrides
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
	overrides := userOverrides(user)
	cfg, warnings, err := Build(b, overrides)
	if err != nil {
		return nil, nil, err
	}
	warnings = append(warnings, OverrideWarnings(auto, user)...)
	return cfg, append(warnings, applyChain(cfg, settings.Chain)...), nil
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

// Build 保留订阅内容及显式覆盖，GLOBAL 和出口选择交给内核。
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
	if base.Get("rules") != nil || user.Get("rules") != nil {
		if err = out.Set(merged, "rules"); err != nil {
			return nil, nil, err
		}
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
	return out, warnings, nil
}
func (m *Manager) apply(ctx context.Context, base, user *Document, settings Settings) error {
	return m.applyTo(ctx, m, base, user, settings)
}

// 配置准备、提交与运行应用按阶段执行；停止态提交后仍保持停止。
func (m *Manager) applyTo(ctx context.Context, target *Manager, base, user *Document, settings Settings) error {
	cfg, _, err := target.buildConfigured(base, user, settings)
	if err != nil {
		return err
	}
	_, live, err := m.managedProcess(ctx)
	if err != nil {
		return err
	}
	binary := target.Store.ResolvePath(settings.MihomoPath)
	_, statErr := os.Stat(binary)
	// 首次下载可以先生成配置；已有内核时必须通过内核校验，权限错误不能跳过。
	if !errors.Is(statErr, os.ErrNotExist) || live {
		binary, err = resolveBinary(binary)
		if err != nil {
			return err
		}
		cfg, err = target.validateWithChain(ctx, binary, cfg, base, user, settings)
		if err != nil {
			return err
		}
	}
	raw, err := cfg.Bytes()
	if err != nil {
		return err
	}
	userBytes, err := user.UserBytes()
	if err != nil {
		return err
	}
	baseBytes, err := base.Bytes()
	if err != nil {
		return err
	}
	sb, err := settings.Bytes()
	if err != nil {
		return err
	}
	if live {
		if err = m.stop(ctx); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	writes := []fileWrite{{"user.yaml", userBytes}, {"subscription.yaml", baseBytes}, {"config.yaml", raw}, {"setting.yaml", sb}}
	if err = commitFiles(target.Store, writes); err != nil {
		return err
	}
	m.Store.dataDir.Store(target.Store.DataDir())
	if err = target.syncService(ctx, binary, target.Store.Path("config.yaml")); err != nil {
		return fmt.Errorf("配置已保存，但同步服务失败: %w", err)
	}
	if live {
		if err = target.launch(ctx, binary, target.Store.Path("config.yaml")); err != nil {
			return fmt.Errorf("配置已保存，但启动内核失败: %w", err)
		}
		// 服务启动命令返回时控制接口可能尚未监听，应用完成前确认新配置已就绪。
		if err = waitAppliedConfig(ctx, cfg); err != nil {
			return fmt.Errorf("配置已保存，但应用就绪检查失败: %w", err)
		}
	}
	return nil
}

func waitAppliedConfig(ctx context.Context, cfg *Document) error {
	controller, err := NewController(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, _, err = controller.Check(ctx, cfg); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), err)
		case <-tick.C:
		}
	}
}
