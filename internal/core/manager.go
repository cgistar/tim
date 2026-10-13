package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Manager struct {
	Store *Store
	HTTP  *http.Client
}
type Status struct {
	Running               bool
	PID                   int
	Mode, Global, Message string
	Chain                 ChainRuntime
	Tun                   bool
	Binary                string
}

func NewManager(s *Store) *Manager {
	return &Manager{Store: s, HTTP: &http.Client{Timeout: 2 * time.Minute}}
}
func (m *Manager) locked(fn func() error) error {
	if err := supported(); err != nil {
		return err
	}
	unlock, err := lockFile(m.Store.Path("operation.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	settings, err := m.Store.Settings()
	if err != nil {
		return err
	}
	configuredDir, err := filepath.EvalSymlinks(m.Store.effectiveDataDir(settings))
	if err != nil {
		return err
	}
	if configuredDir != m.Store.DataDir() {
		return fmt.Errorf("data 目录已在其他操作中改变，请重新打开 tim")
	}
	if m.Store.DataDir() != m.Store.ConfigDir {
		unlockData, err := lockFile(filepath.Join(m.Store.DataDir(), "operation.lock"))
		if err != nil {
			return err
		}
		defer unlockData()
	}
	return fn()
}
func (m *Manager) process() (Process, bool, error) {
	state, err := m.Store.State()
	if err != nil {
		return Process{}, false, err
	}
	if state.Process == nil {
		return Process{}, false, nil
	}
	p := *state.Process
	ok, err := matchesProcess(p)
	return p, ok, err
}
func (m *Manager) Status(ctx context.Context) (Status, error) {
	p, live, err := m.process()
	if err != nil {
		return Status{}, err
	}
	if !live {
		st := Status{Message: "已停止"}
		if raw, readErr := os.ReadFile(m.Store.Path("config.yaml")); readErr == nil {
			d, parseErr := Parse(raw)
			if parseErr != nil {
				return st, parseErr
			}
			st.Chain = ReadChainRuntime(d)
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return st, readErr
		}
		return st, nil
	}
	st := Status{Running: true, PID: p.PID, Binary: p.Binary}
	b, err := os.ReadFile(m.Store.Path("config.yaml"))
	if err != nil {
		return st, err
	}
	d, err := Parse(b)
	if err != nil {
		return st, err
	}
	st.Chain = ReadChainRuntime(d)
	c, err := NewController(d)
	if err != nil {
		return st, err
	}
	info, g, err := c.Check(ctx, d)
	if err == nil {
		err = verifyRuntime(p, d, info)
	}
	st.Mode = info.Mode
	st.Tun = info.Tun.Enable
	st.Global = g
	if err != nil {
		st.Message = "进程存在，但运行检查失败"
		return st, err
	}
	st.Message = "运行中"
	return st, nil
}
func resolveBinary(path string) (string, error) {
	p, err := exec.LookPath(path)
	if err != nil {
		return "", fmt.Errorf("找不到可执行内核，请检查基本配置中的 mihomo 路径: %w", err)
	}
	p, err = filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}
func Version(ctx context.Context, binary string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-v")
	b, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("读取内核版本失败: %w", err)
	}
	v := strings.TrimSpace(string(b))
	if !strings.Contains(v, "Mihomo") {
		return "", fmt.Errorf("可执行文件不是 mihomo")
	}
	return v, nil
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
func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return fmt.Errorf("订阅地址必须是有效 HTTP(S) URL，不支持 URL 用户认证")
	}
	return nil
}
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("响应超过 %d 字节限制", limit)
	}
	return b, nil
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

// 新订阅校验并应用成功后，才同时提交地址和订阅缓存。
func (m *Manager) RefreshURL(ctx context.Context, rawURL string) error {
	return m.locked(func() error {
		s, err := m.Store.Settings()
		if err != nil {
			return err
		}
		s.URL = rawURL
		return m.refresh(ctx, s)
	})
}

func (m *Manager) refresh(ctx context.Context, s Settings) error {
	if err := validateURL(s.URL); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", s.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "tim/1.0")
	started := time.Now()
	res, err := m.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("订阅下载超时（已等待 %s，原缓存保留）：订阅服务或上游未在期限内完成响应: %w", time.Since(started).Round(time.Second), safeNetworkError(err))
		}
		return fmt.Errorf("订阅下载失败（原缓存保留）: %w", safeNetworkError(err))
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("订阅返回 HTTP %d，原缓存保留", res.StatusCode)
	}
	b, err := readBounded(res.Body, 8<<20)
	if err != nil {
		return err
	}
	base, err := Parse(b)
	if err != nil {
		return err
	}
	u, err := m.Store.User()
	if err != nil {
		return err
	}
	_, live, err := m.process()
	if err != nil {
		return err
	}
	return m.apply(ctx, base, u, s, live)
}

func safeNetworkError(err error) error {
	var u *url.Error
	if errors.As(err, &u) {
		return u.Err
	}
	return err
}
func (m *Manager) Candidate() (*Document, error) { return m.Store.Base() }
func (m *Manager) Preview() (*Document, []string, error) {
	return m.Store.Preview()
}

// 保存只更新管理文件；应用运行配置由独立操作执行。
func (m *Manager) SaveManagedSettings(settings Settings) error {
	return m.locked(func() error {
		if err := settings.Validate(); err != nil {
			return err
		}
		raw, err := settings.Bytes()
		if err != nil {
			return err
		}
		return AtomicWrite(m.Store.Path("setting.yaml"), raw, 0600)
	})
}

func (m *Manager) ApplySettings(ctx context.Context, settings Settings) error {
	return m.locked(func() error { return m.applySettings(ctx, settings) })
}
func (m *Manager) applySettings(ctx context.Context, settings Settings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	target := m
	dir := m.Store.effectiveDataDir(settings)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if dir != m.Store.DataDir() {
		store := &Store{ConfigDir: m.Store.ConfigDir, DataOverride: m.Store.DataOverride}
		if err := store.setInitialDataDir(dir); err != nil {
			return err
		}
		target = &Manager{Store: store, HTTP: m.HTTP}
		if runtime.GOOS == "linux" && store.DataDir() != store.ConfigDir {
			unlock, err := lockFile(filepath.Join(store.DataDir(), "operation.lock"))
			if err != nil {
				return err
			}
			defer unlock()
		}
		if _, running, err := target.process(); err != nil {
			return err
		} else if running {
			return fmt.Errorf("目标 data 目录已有运行中的 mihomo，请先停止该实例")
		}
	}
	base, err := target.Candidate()
	if errors.Is(err, os.ErrNotExist) && target != m {
		base, err = m.Candidate()
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	missingBase := errors.Is(err, os.ErrNotExist)
	userStore := target.Store
	if target != m {
		if _, err := os.Stat(target.Store.Path("user.yaml")); errors.Is(err, os.ErrNotExist) {
			userStore = m.Store
		} else if err != nil {
			return err
		}
	}
	user, err := userStore.User()
	if err != nil {
		return err
	}
	old, live, err := m.process()
	if err != nil {
		return err
	}
	if missingBase {
		if live {
			return fmt.Errorf("运行中缺少有效订阅，无法应用配置")
		}
		binary, err := resolveBinary(m.Store.ResolvePath(settings.MihomoPath))
		if err != nil {
			return err
		}
		if _, err = Version(ctx, binary); err != nil {
			return err
		}
		raw, err := settings.Bytes()
		if err != nil {
			return err
		}
		userBytes, err := user.UserBytes()
		if err != nil {
			return err
		}
		if err = commitFiles(target.Store, []fileWrite{{"user.yaml", userBytes}, {"setting.yaml", raw}}); err != nil {
			return err
		}
		m.Store.dataDir.Store(target.Store.DataDir())
		return nil
	}
	if target == m {
		return m.apply(ctx, base, user, settings, live)
	}
	if live {
		if err = m.stop(ctx); err != nil {
			return err
		}
	}
	if err = target.apply(ctx, base, user, settings, live); err != nil {
		if !live {
			return err
		}
		restoreCtx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		if stopErr := target.stop(restoreCtx); stopErr != nil {
			return fmt.Errorf("切换 data 目录失败: %w；停止新实例失败: %v", err, stopErr)
		}
		if restoreErr := m.launch(restoreCtx, old.Binary, m.Store.Path("config.yaml")); restoreErr != nil {
			return fmt.Errorf("切换 data 目录失败: %w；恢复旧实例失败: %v", err, restoreErr)
		}
		return fmt.Errorf("切换 data 目录失败，已恢复旧实例: %w", err)
	}
	m.Store.dataDir.Store(target.Store.DataDir())
	return nil
}
func (m *Manager) setManaged(ctx context.Context, value any, path ...string) error {
	return m.locked(func() error {
		settings, err := m.Store.Settings()
		if err != nil {
			return err
		}
		if err = settings.Config.Set(value, path...); err != nil {
			return err
		}
		return m.applySettings(ctx, settings)
	})
}
func (m *Manager) validate(ctx context.Context, binary string, d *Document) error {
	b, err := d.Bytes()
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(m.Store.Path("home"), ".check-*.yaml")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// 内核首次校验可能下载地理数据库；其单次下载超时为 90 秒，避免提前终止留下半截文件。
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-t", "-d", m.Store.Path("home"), "-f", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("校验超时或已取消（内核可能正在下载 GeoIP/GeoSite 等资源）: %w", ctx.Err())
		}
		return fmt.Errorf("mihomo 配置校验失败: %w\n%s", err, redact(string(out), d.Text("secret")))
	}
	return nil
}

// 进入界面时接管现有实例；优先使用本地订阅，避免每次启动重复下载。
func (m *Manager) AutoStart(ctx context.Context) (string, error) {
	st, err := m.Status(ctx)
	if err != nil {
		return "", err
	}
	if st.Running {
		return "内核正在运行", nil
	}
	_, err = m.Store.Base()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		settings, e := m.Store.Settings()
		if e != nil {
			return "", e
		}
		if settings.URL == "" {
			return "内核已停止", nil
		}
		if e = m.Refresh(ctx); e != nil {
			return "", e
		}
	}
	return "内核已启动", m.Start(ctx)
}
func (m *Manager) Start(ctx context.Context) error {
	return m.locked(func() error {
		_, live, err := m.process()
		if err != nil {
			return err
		}
		if live {
			return fmt.Errorf("mihomo 已运行")
		}
		u, err := m.Store.User()
		if err != nil {
			return err
		}
		b, err := m.Candidate()
		if err != nil {
			return err
		}
		s, err := m.Store.Settings()
		if err != nil {
			return err
		}
		return m.apply(ctx, b, u, s, true)
	})
}
func (m *Manager) ToggleRunning(ctx context.Context) (string, error) {
	_, live, err := m.process()
	if err != nil {
		return "", err
	}
	if live {
		return "内核已停止", m.Stop(ctx)
	}
	return "内核已启动", m.Start(ctx)
}
func (m *Manager) Restart(ctx context.Context) (string, error) {
	return m.applyConfigured(ctx, nil, true)
}
func (m *Manager) Apply(ctx context.Context, u *Document) error {
	_, err := m.applyConfigured(ctx, u, false)
	return err
}
func (m *Manager) applyConfigured(ctx context.Context, u *Document, start bool) (string, error) {
	result := "配置已更新"
	err := m.locked(func() error {
		b, err := m.Candidate()
		if err != nil {
			return err
		}
		if u == nil {
			u, err = m.Store.User()
			if err != nil {
				return err
			}
		}
		s, err := m.Store.Settings()
		if err != nil {
			return err
		}
		_, live, err := m.process()
		if err != nil {
			return err
		}
		if start {
			result = "内核已启动"
			if live {
				result = "内核已重启"
			}
		}
		return m.apply(ctx, b, u, s, live || start)
	})
	return result, err
}
func (m *Manager) ToggleMode(ctx context.Context, mode string) error {
	return m.setManaged(ctx, mode, "mode")
}
func (m *Manager) ToggleTun(ctx context.Context, enabled bool) error {
	if enabled && os.Geteuid() != 0 {
		return fmt.Errorf("TUN 管理需要 sudo/root，请在同一启动目录重新运行 TUI")
	}
	return m.setManaged(ctx, enabled, "tun", "enable")
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
	state, err := m.Store.State()
	if err != nil {
		return rollback(err)
	}
	state.Applied = true
	ab, err := jsonBytes(state)
	if err != nil {
		return rollback(err)
	}
	writes := []fileWrite{{"user.yaml", userBytes}, {"subscription.yaml", baseBytes}, {"setting.yaml", sb}, {"state.json", ab}}
	if err = commitFiles(m.Store, writes); err != nil {
		return rollback(err)
	}

	return nil
}

type fileWrite struct {
	name string
	data []byte
}

func jsonBytes(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
func commitFiles(s *Store, writes []fileWrite) error {
	old := make([][]byte, len(writes))
	exists := make([]bool, len(writes))
	for i, w := range writes {
		b, err := os.ReadFile(s.Path(w.name))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		old[i] = b
		exists[i] = err == nil
	}
	for i, w := range writes {
		var err error
		if w.data == nil {
			if exists[i] {
				err = os.Remove(s.Path(w.name))
			}
		} else {
			err = AtomicWrite(s.Path(w.name), w.data, 0600)
		}
		if err != nil {
			errs := []error{err}
			for j := i - 1; j >= 0; j-- {
				var e error
				if exists[j] {
					e = AtomicWrite(s.Path(writes[j].name), old[j], 0600)
				} else {
					e = os.Remove(s.Path(writes[j].name))
				}
				if e != nil {
					errs = append(errs, e)
				}
			}
			return errors.Join(errs...)
		}
	}
	return nil
}
func (m *Manager) Stop(ctx context.Context) error {
	return m.locked(func() error { return m.stop(ctx) })
}
func (m *Manager) stop(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, live, err := m.process()
	if err != nil {
		return err
	}
	if !live {
		state, e := m.Store.State()
		if e != nil {
			return e
		}
		if state.Process != nil {
			return m.Store.saveProcess(nil)
		}
		return nil
	}
	if err = signalManaged(p); err != nil {
		return err
	}
	// 信号已发出后完成退出等待，取消请求不能留下“旧进程正在退出”的模糊状态。
	ctx = context.WithoutCancel(ctx)
	timer := time.NewTimer(12 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		ok, err := matchesProcess(p)
		if err != nil {
			return err
		}
		if !ok {
			return m.Store.saveProcess(nil)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("mihomo 未在 12 秒内退出，保留进程记录；未强杀")
		case <-tick.C:
		}
	}
}
func (m *Manager) launch(ctx context.Context, binary, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	d, err := Parse(b)
	if err != nil {
		return err
	}
	c, err := NewController(d)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(m.Store.Path("mihomo.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	input, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer input.Close()
	cmd := exec.Command(binary, "-d", m.Store.Path("home"), "-f", path)
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.Stdin = input
	detach(cmd)
	if err = cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	var p Process
	for i := 0; i < 20; i++ {
		stamp, exe, _, e := processIdentity(cmd.Process.Pid)
		if e == nil {
			p = Process{PID: cmd.Process.Pid, Start: stamp, Binary: exe, Runtime: path}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p.PID == 0 {
		return fmt.Errorf("mihomo 启动后立即退出，请查看日志")
	}
	if err = m.Store.saveProcess(&p); err != nil {
		_ = signalManaged(p)
		return err
	}
	readyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var last error
	for {
		ok, e := matchesProcess(p)
		if e != nil {
			return e
		}
		if !ok {
			return fmt.Errorf("mihomo 启动失败并已退出，请查看 mihomo.log")
		}
		info, _, e := c.Check(readyCtx, d)
		if e == nil {
			e = verifyRuntime(p, d, info)
		}
		if e == nil {
			return nil
		}
		last = e
		select {
		case <-readyCtx.Done():
			return fmt.Errorf("启动就绪检查失败: %w", last)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
func (m *Manager) LogTail() (string, error) {
	f, err := os.Open(m.Store.Path("mihomo.log"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if st.Size() > 64<<10 {
		_, err = f.Seek(-(64 << 10), io.SeekEnd)
		if err != nil {
			return "", err
		}
	}
	b, err := readBounded(f, 64<<10)
	return string(b), err
}
