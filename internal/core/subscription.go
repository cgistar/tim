package core

import (
	"bytes"
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
	return m.locked(func() error {
		if hot {
			if _, live, err := m.process(); err != nil {
				return err
			} else if !live {
				return fmt.Errorf("热更新要求 mihomo 正在运行，未下载订阅")
			}
		}
		s, err := m.Store.Settings()
		if err != nil {
			return err
		}
		return m.refreshSubscription(ctx, s, true, hot)
	})
}
func (m *Manager) applyHot(ctx context.Context, base, user *Document, settings Settings) error {
	p, live, err := m.process()
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
	cfg, _, err := BuildConfigured(base, user, settings)
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
		same, err := matchesProcess(p)
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
		return verifyRuntime(p, d, info)
	}
	snapshot := make([]fileWrite, 0, 3)
	for _, name := range []string{"config.yaml", "subscription.yaml", "setting.yaml"} {
		raw, e := os.ReadFile(m.Store.Path(name))
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		snapshot = append(snapshot, fileWrite{name, raw})
	}
	rollback := func(cause error) error {
		recovery, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var restoreErrors []error
		for _, file := range snapshot {
			current, e := os.ReadFile(m.Store.Path(file.name))
			if e == nil && bytes.Equal(current, file.data) {
				continue
			}
			if file.data == nil {
				e = os.Remove(m.Store.Path(file.name))
				if errors.Is(e, os.ErrNotExist) {
					e = nil
				}
			} else {
				e = AtomicWrite(m.Store.Path(file.name), file.data, 0600)
			}
			if e != nil {
				restoreErrors = append(restoreErrors, fmt.Errorf("恢复 %s 失败: %w", file.name, e))
			}
		}
		if e := controller.Do(recovery, "PUT", "/configs?force=true", map[string]string{"payload": string(oldRaw)}, nil); e != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("恢复运行配置失败: %w", e))
		} else if e = check(recovery, old); e != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("恢复运行检查失败: %w", e))
		}
		if len(restoreErrors) > 0 {
			return errors.Join(append([]error{cause}, restoreErrors...)...)
		}
		return fmt.Errorf("热更新失败，已恢复旧运行配置及文件: %w", cause)
	}
	if err = controller.Do(ctx, "PUT", "/configs?force=true", map[string]string{"payload": string(raw)}, nil); err != nil {
		return rollback(err)
	}
	if err = check(ctx, cfg); err != nil {
		return rollback(err)
	}
	baseRaw, err := base.Bytes()
	if err != nil {
		return rollback(err)
	}
	settingsRaw, err := settings.Bytes()
	if err != nil {
		return rollback(err)
	}
	if err = commitFiles(m.Store, []fileWrite{{"config.yaml", raw}, {"subscription.yaml", baseRaw}, {"setting.yaml", settingsRaw}}); err != nil {
		return rollback(err)
	}
	return nil
}

func (m *Manager) SaveSettings(v Settings) error {
	return m.locked(func() error {
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
	return m.locked(func() error {
		s, err := m.Store.Settings()
		if err != nil {
			return err
		}
		return m.refresh(ctx, s)
	})
}

// 地址、下载缓存与运行配置分别提交，后续失败不撤销已完成的阶段。
func (m *Manager) RefreshURL(ctx context.Context, rawURL string) error {
	return m.locked(func() error {
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
	return m.refreshSubscription(ctx, s, false, false)
}
func (m *Manager) refreshSubscription(ctx context.Context, s Settings, forceStart, hot bool) error {
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
	if err = m.applySubscription(ctx, base, s, forceStart, hot); err != nil {
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

func (m *Manager) applySubscription(ctx context.Context, base *Document, s Settings, forceStart, hot bool) error {
	_, live, err := m.process()
	if err != nil {
		return err
	}
	if !live && !forceStart && !hot {
		return nil
	}
	u, err := m.Store.User()
	if err != nil {
		return err
	}
	if hot {
		return m.applyHot(ctx, base, u, s)
	}
	return m.apply(ctx, base, u, s, live || forceStart)
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
	return m.Store.Preview()
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
func (m *Manager) apply(ctx context.Context, base, user *Document, settings Settings, start bool) error {
	cfg, _, err := BuildConfigured(base, user, settings)
	if err != nil {
		return err
	}
	binary, err := resolveBinary(m.Store.ResolvePath(settings.MihomoPath))
	if err != nil {
		return err
	}
	if cfg.Text("tun", "enable") == "true" && start && os.Geteuid() != 0 {
		return fmt.Errorf("启用 TUN 需要 sudo/root")
	}
	if cfg, err = m.validateWithChain(ctx, binary, cfg, base, user, settings); err != nil {
		return err
	}
	raw, err := cfg.Bytes()
	if err != nil {
		return err
	}
	runtimePath := m.Store.Path("config.yaml")
	previous, readErr := os.ReadFile(runtimePath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	old, live, err := m.process()
	if err != nil {
		return err
	}
	if live {
		if err = m.stop(ctx); err != nil {
			return err
		}
	}
	rollback := func(cause error) error {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, running, e := m.process(); e != nil {
			return fmt.Errorf("应用失败: %w；无法确认新进程状态: %v", cause, e)
		} else if running {
			if e = m.stop(rollbackCtx); e != nil {
				return fmt.Errorf("应用失败: %w；停止新进程失败: %v", cause, e)
			}
		}
		if e := m.stop(rollbackCtx); e != nil {
			return fmt.Errorf("应用失败: %w；清理进程状态失败: %v", cause, e)
		}
		var restoreErr error
		if readErr == nil {
			restoreErr = AtomicWrite(runtimePath, previous, 0600)
		} else {
			restoreErr = os.Remove(runtimePath)
			if errors.Is(restoreErr, os.ErrNotExist) {
				restoreErr = nil
			}
		}
		if restoreErr != nil {
			return fmt.Errorf("应用失败: %w；恢复 config.yaml 失败: %v", cause, restoreErr)
		}
		if live {
			if e := m.launch(rollbackCtx, old.Binary, runtimePath); e != nil {
				return fmt.Errorf("应用失败: %w；旧配置恢复失败: %v", cause, e)
			}
			return fmt.Errorf("应用失败，已恢复旧运行配置: %w", cause)
		}
		return cause
	}
	if err = AtomicWrite(runtimePath, raw, 0600); err != nil {
		return rollback(err)
	}
	if start {
		if err = m.launch(ctx, binary, runtimePath); err != nil {
			return rollback(err)
		}
	}
	// 提交前保存原始文件，提交失败同样恢复文件和运行实例。
	userBytes, err := user.UserBytes()
	if err != nil {
		return rollback(err)
	}
	baseBytes, err := base.Bytes()
	if err != nil {
		return rollback(err)
	}
	sb, err := settings.Bytes()
	if err != nil {
		return rollback(err)
	}
	writes := []fileWrite{{"user.yaml", userBytes}, {"subscription.yaml", baseBytes}, {"setting.yaml", sb}}
	if err = commitFiles(m.Store, writes); err != nil {
		return rollback(err)
	}

	return nil
}
