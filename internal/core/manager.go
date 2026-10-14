package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	return discoverProcess(m.Store.Path("config.yaml"), m.Store.Path("home"))
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
			return nil
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
		return fmt.Errorf("TUN 管理需要 sudo/root，请以管理员身份重新运行同一个 tim 程序")
	}
	return m.setManaged(ctx, enabled, "tun", "enable")
}

type fileWrite struct {
	name string
	data []byte
}

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
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("mihomo 未在 12 秒内退出，未强杀")
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
