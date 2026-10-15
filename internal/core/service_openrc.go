package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func newOpenRCService() *initService {
	return &initService{kind: serviceOpenRC, dir: "/etc/init.d", check: func() error {
		if err := supported(); err != nil {
			return err
		}
		if os.Geteuid() != 0 {
			return fmt.Errorf("安装或管理系统服务需要 sudo/root")
		}
		if _, err := os.Stat("/run/openrc/softlevel"); err != nil {
			return fmt.Errorf("当前未运行 OpenRC: %w", err)
		}
		for _, command := range []string{"rc-service", "rc-update", "rc-status", "supervise-daemon", "/sbin/openrc-run"} {
			if _, err := exec.LookPath(command); err != nil {
				return fmt.Errorf("OpenRC 服务管理需要 %s: %w", command, err)
			}
		}
		return nil
	}, run: func(ctx context.Context, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env = append(os.Environ(), "LC_ALL=C", "RC_NOCOLOR=yes")
		return cmd.CombinedOutput()
	}}
}

func (m *Manager) openRCCommand(ctx context.Context, args ...string) ([]byte, error) {
	switch args[0] {
	case "--version":
		return m.service.run(ctx, "rc-service", "--version")
	case "daemon-reload":
		// OpenRC 每次读取脚本，只需刷新依赖缓存。
		return m.service.run(ctx, "rc-update", "--update")
	case "start", "stop":
		return m.service.run(ctx, "rc-service", "--nocolor", args[1], args[0])
	case "enable":
		return m.service.run(ctx, "rc-update", "add", args[1], "default")
	case "disable":
		levels, err := m.openRCRunlevels(ctx, args[1])
		if err != nil {
			return nil, err
		}
		// 只删除实际登记的 runlevel，避免 OpenRC 将重复禁用报告为失败。
		for _, level := range levels {
			if out, err := m.service.run(ctx, "rc-update", "del", args[1], level); err != nil {
				return out, err
			}
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("不支持的 OpenRC 操作: %s", args[0])
	}
}

func (m *Manager) openRCRunlevels(ctx context.Context, name string) ([]string, error) {
	out, err := m.service.run(ctx, "rc-update", "show", "-v")
	if err != nil {
		return nil, fmt.Errorf("读取 OpenRC 开机启动状态失败: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		service, levels, ok := strings.Cut(line, "|")
		if ok && strings.TrimSpace(service) == name {
			return strings.Fields(levels), nil
		}
	}
	return nil, fmt.Errorf("OpenRC 未列出当前服务的 runlevel 状态: %s", name)
}

func (m *Manager) readOpenRCStatus(ctx context.Context, st ServiceStatus) (ServiceStatus, error) {
	out, err := m.service.run(ctx, "rc-service", "--resolve", st.Name)
	if err != nil {
		return st, fmt.Errorf("OpenRC 未正确加载当前服务: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) != m.servicePath() {
		return st, fmt.Errorf("OpenRC 加载的服务路径不匹配: %s", strings.TrimSpace(string(out)))
	}
	levels, err := m.openRCRunlevels(ctx, st.Name)
	if err != nil {
		return st, err
	}
	st.Enabled = len(levels) > 0
	out, err = m.service.run(ctx, "rc-status", "--format", "ini", "--servicelist")
	if err != nil {
		return st, fmt.Errorf("读取 OpenRC 服务状态失败: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != st.Name {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			break
		}
		st.State, st.Result = fields[0], "success"
		switch st.State {
		case "started", "starting", "stopping", "scheduled":
			st.Active = true
		case "stopped", "inactive":
		case "crashed", "unsupervised", "failed":
			st.Result = st.State
		default:
			return st, fmt.Errorf("未知的 OpenRC 服务状态: %s", value)
		}
		return st, nil
	}
	return st, fmt.Errorf("OpenRC 未列出当前服务状态: %s", st.Name)
}

func shellValue(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n\t") {
		return "", fmt.Errorf("服务路径不能包含换行、制表符或 NUL")
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'", nil
}

func openRCServiceScript(spec serviceSpec) ([]byte, error) {
	marker, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	values := make([]string, 0, 4)
	for _, value := range []string{spec.Binary, spec.Home, spec.Runtime, spec.Log} {
		quoted, err := shellValue(value)
		if err != nil {
			return nil, err
		}
		values = append(values, quoted)
	}
	// 直接传递参数以避开 openrc-run 默认 start 中的 eval；不登记内核 PID。
	return []byte(fmt.Sprintf(`#!/sbin/openrc-run
%s%s
description="tim mihomo"
supervisor="supervise-daemon"

depend() {
  need localmount
  after bootmisc networking
}

start() {
  local help
  help=$(supervise-daemon "$RC_SVCNAME" --help) || return $?
  case "$help" in
    *--respawn-delay-step*) set -- --respawn-delay-step 0 ;;
    *) set -- ;;
  esac
  ebegin "Starting $RC_SVCNAME"
  supervise-daemon "$RC_SVCNAME" --start --respawn-delay 3 --respawn-max 0 "$@" \
    --retry TERM/12/KILL/3 --umask 077 --chdir %s --stdout %s --stderr %s \
    %s -- -d %s -f %s
  eend $?
}
`, serviceMarker, marker, values[1], values[3], values[3], values[0], values[1], values[2])), nil
}
