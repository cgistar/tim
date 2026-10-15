package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const serviceMarker = "# tim-service: "

type ServiceStatus struct {
	Installed, Enabled, Active bool
	Name, State, Result        string
	Manager                    string
}

type serviceSpec struct {
	ConfigDir string `json:"config_dir"`
	Binary    string `json:"binary"`
	Runtime   string `json:"runtime"`
	Home      string `json:"home"`
	Log       string `json:"log"`
}

type initService struct {
	kind  string
	dir   string
	run   func(context.Context, ...string) ([]byte, error)
	check func() error
}

const (
	serviceSystemd = "systemd"
	serviceOpenRC  = "OpenRC"
)

func selectServiceKind(systemdRunning, openrcRunning bool) string {
	if systemdRunning {
		return serviceSystemd
	}
	if openrcRunning {
		return serviceOpenRC
	}
	return ""
}

func newServiceManager() *initService {
	_, systemdErr := os.Stat("/run/systemd/system")
	_, openrcErr := os.Stat("/run/openrc/softlevel")
	switch selectServiceKind(systemdErr == nil, openrcErr == nil) {
	case serviceSystemd:
		return newSystemdService()
	case serviceOpenRC:
		return newOpenRCService()
	default:
		s := newSystemdService()
		s.check = func() error { return fmt.Errorf("服务管理需要正在运行的 systemd 或 OpenRC") }
		return s
	}
}

func newSystemdService() *initService {
	return &initService{kind: serviceSystemd, dir: "/etc/systemd/system", check: func() error {
		if err := supported(); err != nil {
			return err
		}
		if os.Geteuid() != 0 {
			return fmt.Errorf("安装或管理系统服务需要 sudo/root")
		}
		if _, err := os.Stat("/run/systemd/system"); err != nil {
			return fmt.Errorf("当前未运行 systemd: %w", err)
		}
		return nil
	}, run: func(ctx context.Context, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "systemctl", append([]string{"--no-ask-password", "--no-pager"}, args...)...).CombinedOutput()
	}}
}

func (m *Manager) serviceName() string {
	sum := sha256.Sum256([]byte(m.Store.ConfigDir))
	if m.service.kind == serviceOpenRC {
		return fmt.Sprintf("tim-mihomo-%x", sum[:8])
	}
	return fmt.Sprintf("tim-mihomo-%x.service", sum[:8])
}

func (m *Manager) servicePath() string {
	return filepath.Join(m.service.dir, m.serviceName())
}

func (m *Manager) serviceFile() ([]byte, serviceSpec, error) {
	var spec serviceSpec
	info, err := os.Lstat(m.servicePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, spec, nil
	}
	if err != nil {
		return nil, spec, err
	}
	if !info.Mode().IsRegular() {
		return nil, spec, fmt.Errorf("服务文件不是普通文件，拒绝操作: %s", m.servicePath())
	}
	raw, err := os.ReadFile(m.servicePath())
	if err != nil {
		return nil, spec, err
	}
	definition := string(raw)
	if m.service.kind == serviceOpenRC {
		if !strings.HasPrefix(definition, "#!/sbin/openrc-run\n") {
			return nil, spec, fmt.Errorf("服务文件不是当前 tim 的 OpenRC 脚本，拒绝操作: %s", m.servicePath())
		}
		definition = strings.TrimPrefix(definition, "#!/sbin/openrc-run\n")
	}
	line, _, _ := strings.Cut(definition, "\n")
	if !strings.HasPrefix(line, serviceMarker) || json.Unmarshal([]byte(strings.TrimPrefix(line, serviceMarker)), &spec) != nil || spec.ConfigDir != m.Store.ConfigDir {
		return nil, spec, fmt.Errorf("服务文件不属于当前 tim 配置目录，拒绝覆盖或移除: %s", m.servicePath())
	}
	return raw, spec, nil
}

func (m *Manager) serviceCommand(ctx context.Context, args ...string) ([]byte, error) {
	if m.root != nil && len(args) > 0 && args[0] != "show" {
		reply, err := m.rootCall(ctx, rootRequest{Action: "service-command", Args: args})
		return reply.Output, err
	}
	var out []byte
	var err error
	if m.service.kind == serviceOpenRC {
		out, err = m.openRCCommand(ctx, args...)
	} else {
		out, err = m.service.run(ctx, args...)
	}
	if err != nil {
		return nil, fmt.Errorf("%s %s 失败: %w\n%s", m.service.kind, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (m *Manager) ServiceStatus(ctx context.Context) (ServiceStatus, error) {
	st := ServiceStatus{Name: m.serviceName(), Manager: m.service.kind}
	raw, spec, err := m.serviceFile()
	if err != nil || raw == nil {
		return st, err
	}
	st.Installed = true
	if spec.Runtime != m.Store.Path("config.yaml") || spec.Home != m.Store.Path("home") {
		return st, fmt.Errorf("服务使用其他 data 目录，请使用安装时的 DATA 或已保存的 data-dir 管理")
	}
	return m.readServiceStatus(ctx, st)
}

func (m *Manager) readServiceStatus(ctx context.Context, st ServiceStatus) (ServiceStatus, error) {
	st.Manager = m.service.kind
	if m.service.kind == serviceOpenRC {
		return m.readOpenRCStatus(ctx, st)
	}
	out, err := m.serviceCommand(ctx, "show", st.Name, "--property=LoadState,FragmentPath,UnitFileState,ActiveState,SubState,Result")
	if err != nil {
		return st, err
	}
	props := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			props[key] = value
		}
	}
	if props["LoadState"] != "loaded" || props["FragmentPath"] != m.servicePath() || props["ActiveState"] == "" || props["UnitFileState"] == "" {
		return st, fmt.Errorf("systemd 未正确加载当前服务: %s", strings.TrimSpace(string(out)))
	}
	st.Enabled = props["UnitFileState"] == "enabled"
	st.State = props["ActiveState"] + "/" + props["SubState"]
	st.Result = props["Result"]
	st.Active = props["ActiveState"] == "active" || props["ActiveState"] == "activating" || props["ActiveState"] == "deactivating"
	return st, nil
}

// 服务状态只补充自动重启意图，进程身份仍由唯一的 /proc 判断负责。
func (m *Manager) managedProcess(ctx context.Context) (Process, bool, error) {
	p, live, err := m.processContext(ctx)
	if err != nil {
		return p, live, err
	}
	raw, spec, err := m.serviceFile()
	if err != nil {
		return p, live, err
	}
	var svc ServiceStatus
	if raw != nil {
		svc, err = m.readServiceStatus(ctx, ServiceStatus{Installed: true, Name: m.serviceName()})
		if err != nil {
			return p, live, err
		}
	}
	if !live && svc.Active {
		p.Binary, p.Runtime = spec.Binary, spec.Runtime
	}
	return p, live || svc.Active, nil
}

func unitValue(value string, command bool) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n\t") {
		return "", fmt.Errorf("服务路径不能包含换行、制表符或 NUL")
	}
	value = strings.ReplaceAll(value, "%", "%%")
	// 路径指令按整行解析，不移除引号；只有 ExecStart 使用参数引用规则。
	if !command {
		return value, nil
	}
	value = strings.ReplaceAll(value, "$", "$$")
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	value = strings.ReplaceAll(value, "\t", "\\t")
	return "\"" + value + "\"", nil
}

func serviceUnit(spec serviceSpec) ([]byte, error) {
	marker, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, 5)
	for _, value := range []string{spec.Binary, "-d", spec.Home, "-f", spec.Runtime} {
		v, err := unitValue(value, true)
		if err != nil {
			return nil, err
		}
		args = append(args, v)
	}
	work, err := unitValue(spec.Home, false)
	if err != nil {
		return nil, err
	}
	log, err := unitValue("append:"+spec.Log, false)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("%s%s\n[Unit]\nDescription=tim mihomo\nAfter=network.target\nStartLimitIntervalSec=0\n\n[Service]\nType=exec\nExecStart=%s\nWorkingDirectory=%s\nRestart=always\nRestartSec=3s\nTimeoutStopSec=12s\nKillMode=control-group\nStandardOutput=%s\nStandardError=inherit\n\n[Install]\nWantedBy=multi-user.target\n", serviceMarker, marker, strings.Join(args, " "), work, log)), nil
}

func (m *Manager) spec(binary, path string) serviceSpec {
	return serviceSpec{ConfigDir: m.Store.ConfigDir, Binary: binary, Runtime: path, Home: m.Store.Path("home"), Log: m.Store.Path("mihomo.log")}
}

func (m *Manager) requireService(ctx context.Context) error {
	if err := m.service.check(); err != nil {
		return err
	}
	_, err := m.serviceCommand(ctx, "--version")
	return err
}

func (m *Manager) InstallService(ctx context.Context) error {
	if m.root != nil {
		_, err := m.rootCall(ctx, rootRequest{Action: "service-install"})
		return err
	}
	return m.withOperation(func() error {
		if err := m.requireService(ctx); err != nil {
			return err
		}
		svc, err := m.ServiceStatus(ctx)
		if err != nil {
			return err
		}
		_, live, err := m.processContext(ctx)
		if err != nil {
			return err
		}
		if live || svc.Active {
			return fmt.Errorf("请先停止内核，再安装服务")
		}
		if svc.Installed {
			return fmt.Errorf("服务已安装")
		}
		binary, err := m.Store.Kernel()
		if err != nil {
			return err
		}
		raw, err := m.serviceDefinition(m.spec(binary, m.Store.Path("config.yaml")))
		if err != nil {
			return err
		}
		if err = AtomicWrite(m.servicePath(), raw, m.serviceMode()); err != nil {
			return err
		}
		if _, err = m.serviceCommand(ctx, "daemon-reload"); err != nil {
			return errors.Join(err, m.restoreService(context.WithoutCancel(ctx), nil))
		}
		if _, err = m.ServiceStatus(ctx); err != nil {
			return errors.Join(err, m.restoreService(context.WithoutCancel(ctx), nil))
		}
		return nil
	})
}

// 确认由 CLI/TUI 负责；共享操作在停止成功前不删除服务文件。
func (m *Manager) UninstallService(ctx context.Context) error {
	if m.root != nil {
		_, err := m.rootCall(ctx, rootRequest{Action: "service-uninstall"})
		return err
	}
	return m.withOperation(func() error {
		previous, _, err := m.serviceFile()
		if err != nil {
			return err
		}
		svc, err := m.ServiceStatus(ctx)
		if err != nil {
			return err
		}
		if !svc.Installed {
			return fmt.Errorf("服务未安装")
		}
		if err = m.requireService(ctx); err != nil {
			return err
		}
		if err = m.stop(ctx); err != nil {
			return err
		}
		if _, err = m.serviceCommand(ctx, "disable", svc.Name); err != nil {
			return err
		}
		if err = os.Remove(m.servicePath()); err != nil {
			return err
		}
		_, err = m.serviceCommand(context.WithoutCancel(ctx), "daemon-reload")
		if err != nil {
			return errors.Join(err, m.restoreService(context.WithoutCancel(ctx), previous))
		}
		return err
	})
}

func (m *Manager) restoreService(ctx context.Context, raw []byte) error {
	var err error
	if raw == nil {
		err = os.Remove(m.servicePath())
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else {
		err = AtomicWrite(m.servicePath(), raw, m.serviceMode())
	}
	if err != nil {
		return err
	}
	_, err = m.serviceCommand(ctx, "daemon-reload")
	return err
}

func (m *Manager) syncService(ctx context.Context, binary, path string) error {
	previous, _, err := m.serviceFile()
	if err != nil || previous == nil {
		return err
	}
	if m.root != nil {
		_, err := m.rootCall(ctx, rootRequest{Action: "service-sync"})
		return err
	}
	raw, err := m.serviceDefinition(m.spec(binary, path))
	if err != nil {
		return err
	}
	if !bytes.Equal(previous, raw) {
		if err = AtomicWrite(m.servicePath(), raw, m.serviceMode()); err != nil {
			return err
		}
	}
	// 上次可能已经保存定义但重载失败，重新应用时仍须通知服务管理器。
	_, err = m.serviceCommand(ctx, "daemon-reload")
	return err
}

func (m *Manager) serviceDefinition(spec serviceSpec) ([]byte, error) {
	if m.service.kind == serviceOpenRC {
		return openRCServiceScript(spec)
	}
	return serviceUnit(spec)
}

func (m *Manager) serviceMode() os.FileMode {
	if m.service.kind == serviceOpenRC {
		return 0755
	}
	return 0644
}

func (m *Manager) enableService(ctx context.Context) error {
	raw, _, err := m.serviceFile()
	if err != nil || raw == nil {
		return err
	}
	_, err = m.serviceCommand(ctx, "enable", m.serviceName())
	return err
}
