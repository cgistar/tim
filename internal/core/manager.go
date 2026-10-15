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
	"strings"
	"syscall"
	"time"
)

type Manager struct {
	root     *RootSession
	Store    *Store
	HTTP     *http.Client
	IPClient *http.Client
	service  *initService
	guard    *networkGuard
}
type Status struct {
	Running               bool
	Unknown               bool
	PID                   int
	Mode, Global, Message string
	Chain                 ChainRuntime
	Tun                   bool
	Binary                string
	Service               ServiceStatus
	Guard                 GuardStatus
}

func NewManager(s *Store) *Manager {
	m := &Manager{Store: s, service: newServiceManager(), guard: newNetworkGuard()}
	m.HTTP = &http.Client{Timeout: 2 * time.Minute, Transport: guardTransport{manager: m}}
	m.IPClient = &http.Client{Timeout: 8 * time.Second}
	return m
}
func (m *Manager) withOperation(fn func() error) error {
	if err := supported(); err != nil {
		return err
	}
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
	if raw, spec, err := m.serviceFile(); err != nil {
		return err
	} else if raw != nil && (spec.Runtime != m.Store.Path("config.yaml") || spec.Home != m.Store.Path("home")) {
		return fmt.Errorf("服务使用其他 data 目录，请使用安装时的 DATA 或已保存的 data-dir 管理")
	}
	return fn()
}
func (m *Manager) process() (Process, bool, error) {
	return discoverProcess(m.Store.Path("config.yaml"), m.Store.Path("home"))
}
func (m *Manager) processContext(ctx context.Context) (Process, bool, error) {
	p, live, err := m.process()
	if m.root == nil || !errors.Is(err, os.ErrPermission) {
		return p, live, err
	}
	reply, err := m.rootCall(ctx, rootRequest{Action: "process"})
	return reply.Process, reply.Live, err
}
func (m *Manager) Status(ctx context.Context) (status Status, result error) {
	defer func() { status.Guard, _ = m.readNetworkGuardStatus(ctx) }()
	svc, serviceErr := m.ServiceStatus(ctx)
	p, live, processErr := discoverProcessStatus(m.Store.Path("config.yaml"), m.Store.Path("home"))
	st := Status{Service: svc, Running: live, Unknown: processErr != nil, PID: p.PID, Binary: p.Binary, Message: "已停止"}
	if st.Unknown {
		st.Message = "进程状态未知"
	} else if live {
		st.Message = "进程存在，但控制接口不可用"
	} else if svc.Active {
		st.Message = "服务正在恢复: " + svc.State
	} else if svc.Installed && svc.Result != "" && svc.Result != "success" {
		st.Message = "服务失败: " + svc.Result
	}
	raw, err := os.ReadFile(m.Store.Path("config.yaml"))
	if errors.Is(err, os.ErrNotExist) && !live {
		return st, errors.Join(processErr, serviceErr)
	}
	if err != nil {
		return st, errors.Join(processErr, serviceErr, err)
	}
	cfg, err := Parse(raw)
	if err != nil {
		return st, errors.Join(processErr, serviceErr, err)
	}
	st.Chain = ReadChainRuntime(cfg)
	if !live && cfg.Text("external-controller") == "" {
		return st, errors.Join(processErr, serviceErr)
	}
	c, err := NewController(cfg)
	if err != nil {
		return st, errors.Join(processErr, serviceErr, err)
	}
	var info RuntimeInfo
	if err = c.Do(ctx, "GET", "/configs", nil, &info); err != nil {
		if !live && errors.Is(err, syscall.ECONNREFUSED) {
			return st, errors.Join(processErr, serviceErr)
		}
		if !live {
			st.Unknown = true
			st.Message = "运行状态未知"
		}
		return st, errors.Join(processErr, serviceErr, err)
	}
	// 控制接口响应即可显示实际状态；信号/配置操作仍执行严格进程核验。
	st.Running, st.Unknown, st.Message = true, false, "运行中"
	st.Mode, st.Tun = info.Mode, info.Tun.Enable
	if info.Mode != "rule" && info.Mode != "global" && info.Mode != "direct" {
		return st, errors.Join(serviceErr, fmt.Errorf("控制接口返回无效模式 %q", info.Mode))
	}
	if info.Mode == "global" {
		g, err := c.Global(ctx)
		st.Global = g.Now
		if err != nil {
			return st, errors.Join(serviceErr, err)
		}
	}
	return st, serviceErr
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
	return m.withOperation(func() error {
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
	return m.withOperation(func() error { return m.applySettings(ctx, settings) })
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
		target = &Manager{Store: store, HTTP: m.HTTP, service: m.service, guard: m.guard}
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
	_, live, err := m.managedProcess(ctx)
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
		return target.syncService(ctx, binary, target.Store.Path("config.yaml"))
	}
	return m.applyTo(ctx, target, base, user, settings)
}
func (m *Manager) setManaged(ctx context.Context, value any, path ...string) error {
	return m.withOperation(func() error {
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

func (m *Manager) Start(ctx context.Context) error {
	return m.withOperation(func() error {
		_, err := m.start(ctx, false)
		return err
	})
}

// 启停只使用已保存的运行配置，不读取订阅或重新合并用户设置。
func (m *Manager) start(ctx context.Context, restart bool) (string, error) {
	_, live, err := m.processContext(ctx)
	if err != nil {
		return "", err
	}
	svc, err := m.ServiceStatus(ctx)
	if err != nil {
		return "", err
	}
	if live && svc.Installed && !svc.Active {
		return "", fmt.Errorf("服务未运行，但发现匹配的内核进程；请先停止外部启动的内核，再执行 tim start")
	}
	if live && !restart {
		return "内核正在运行", m.enableService(ctx)
	}
	path := m.Store.Path("config.yaml")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("缺少 config.yaml，请先下载订阅或应用配置: %w", err)
	}
	if err != nil {
		return "", err
	}
	if _, err = Parse(raw); err != nil {
		return "", fmt.Errorf("读取 config.yaml 失败: %w", err)
	}
	binary, err := m.Store.Kernel()
	if err != nil {
		return "", err
	}
	message := "内核已启动"
	if restart && (live || svc.Active) {
		if err = m.stop(ctx); err != nil {
			return "", err
		}
		message = "内核已重启"
	}
	if err = m.launch(ctx, binary, path); err != nil {
		return "", err
	}
	return message, m.enableService(ctx)
}

func (m *Manager) ToggleRunning(ctx context.Context) (string, error) {
	_, live, err := m.managedProcess(ctx)
	if err != nil {
		return "", err
	}
	if live {
		return "内核已停止", m.Stop(ctx)
	}
	return "内核已启动", m.Start(ctx)
}
func (m *Manager) Restart(ctx context.Context) (string, error) {
	var message string
	err := m.withOperation(func() error {
		var err error
		message, err = m.start(ctx, true)
		return err
	})
	return message, err
}
func (m *Manager) Apply(ctx context.Context, u *Document) error {
	return m.withOperation(func() error {
		base, err := m.Candidate()
		if err != nil {
			return err
		}
		if u == nil {
			u, err = m.Store.User()
			if err != nil {
				return err
			}
		}
		settings, err := m.Store.Settings()
		if err != nil {
			return err
		}
		return m.apply(ctx, base, u, settings)
	})
}
func (m *Manager) ToggleMode(ctx context.Context, mode string) error {
	return m.setManaged(ctx, mode, "mode")
}
func (m *Manager) ToggleTun(ctx context.Context, enabled bool) error {
	if enabled && os.Geteuid() != 0 && m.root == nil {
		return fmt.Errorf("TUN 管理需要 sudo/root，请以管理员身份重新运行同一个 tim 程序")
	}
	return m.setManaged(ctx, enabled, "tun", "enable")
}

type fileWrite struct {
	name string
	data []byte
}

// 每个文件原子替换；后续步骤失败时保留已完成的写入。
func commitFiles(s *Store, writes []fileWrite) error {
	for _, w := range writes {
		if err := AtomicWrite(s.Path(w.name), w.data, 0600); err != nil {
			return fmt.Errorf("保存 %s 失败（已完成的写入保留）: %w", w.name, err)
		}
	}
	return nil
}
func (m *Manager) Stop(ctx context.Context) error {
	return m.withOperation(func() error {
		raw, _, err := m.serviceFile()
		if err != nil {
			return err
		}
		if err = m.stop(ctx); err != nil {
			return err
		}
		if raw != nil {
			if _, err = m.serviceCommand(ctx, "disable", m.serviceName()); err != nil {
				return fmt.Errorf("服务已停止，但取消开机启动失败: %w", err)
			}
		}
		return nil
	})
}
func (m *Manager) stop(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, live, err := m.processContext(ctx)
	if err != nil {
		return err
	}
	raw, _, err := m.serviceFile()
	if err != nil {
		return err
	}
	if raw != nil {
		if _, err = m.serviceCommand(context.WithoutCancel(ctx), "stop", m.serviceName()); err != nil {
			return err
		}
		_, running, err := m.processContext(ctx)
		if err != nil {
			return err
		}
		if running {
			return fmt.Errorf("服务已停止，但匹配内核仍在运行，拒绝继续操作")
		}
		return nil
	}
	if !live {
		return nil
	}
	if err = m.signalProcess(ctx, p); err != nil {
		return err
	}
	// 信号已发出后完成退出等待，取消请求不能留下“旧进程正在退出”的模糊状态。
	ctx = context.WithoutCancel(ctx)
	timer := time.NewTimer(12 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		ok, err := m.matchesManaged(ctx, p)
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
	raw, _, err := m.serviceFile()
	if err != nil {
		return err
	}
	if raw != nil {
		_, err = m.serviceCommand(ctx, "start", m.serviceName())
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	d, err := Parse(b)
	if err != nil {
		return err
	}
	if d.Text("tun", "enable") == "true" && os.Geteuid() != 0 {
		if m.root != nil {
			// 日志由界面用户创建；助手只负责启动需要 TUN 权限的内核。
			f, e := os.OpenFile(m.Store.Path("mihomo.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			if e = f.Close(); e != nil {
				return e
			}
			_, e = m.rootCall(ctx, rootRequest{Action: "launch"})
			return e
		}
		return fmt.Errorf("启用 TUN 需要 sudo/root")
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
		ok, e := m.matchesManaged(ctx, p)
		if e != nil {
			return e
		}
		if !ok {
			return fmt.Errorf("mihomo 启动失败并已退出，请查看 mihomo.log")
		}
		info, _, e := c.Check(readyCtx, d)
		if e == nil {
			e = m.verifyProcess(ctx, p, d, info)
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
