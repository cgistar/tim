package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"tim/internal/core"
	"tim/internal/ui"
)

const usage = `用法: tim [install|uninstall|start|stop|restart|status|update-sub [--hot]]
       tim guard [status|on|off [--yes]]
无参数打开 TUI。
配置固定为 tim 实际可执行文件所在目录的 setting.yaml；DATA 优先于 setting.yaml 的 data-dir。
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
func run(args []string, out, errout io.Writer) int {
	return runInput(args, os.Stdin, out, errout)
}
func runInput(args []string, input io.Reader, out, errout io.Writer) int {
	if len(args) == 2 && args[0] == "--root-helper" {
		executable, err := os.Executable()
		if err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		directory, err := executableConfigDir(executable)
		if err != nil || directory != args[1] {
			fmt.Fprintln(errout, "root 助手只能管理当前 tim 所在目录")
			return 1
		}
		if err := core.RunRootHelper(args[1], input, out); err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		return 0
	}
	if len(args) > 0 && args[0] == "guard" {
		return runGuard(args[1:], input, out, errout)
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(out, usage)
		return 0
	}
	action := ""
	hot := false
	if len(args) > 0 {
		action = args[0]
		switch action {
		case "install", "uninstall", "start", "stop", "restart", "status":
			if len(args) != 1 {
				fmt.Fprint(errout, usage)
				return 2
			}
		case "update-sub":
			if len(args) == 2 && args[1] == "--hot" {
				hot = true
			} else if len(args) != 1 {
				fmt.Fprint(errout, usage)
				return 2
			}
		default:
			fmt.Fprint(errout, usage)
			return 2
		}
	}
	if runtime.GOOS != "linux" {
		fmt.Fprintln(errout, "tim 进程管理仅支持 Linux 5.3+")
		return 1
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(errout, "定位 tim 可执行文件:", err)
		return 1
	}
	configDir, err := executableConfigDir(executable)
	if err != nil {
		fmt.Fprintln(errout, "解析 tim 配置目录:", err)
		return 1
	}
	if action != "" {
		if _, err := os.Stat(filepath.Join(configDir, "setting.yaml")); err != nil {
			fmt.Fprintln(errout, "tim 所在目录缺少可读取的 setting.yaml:", err)
			return 1
		}
	}
	s, err := core.Open(configDir)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	m := core.NewManager(s)
	if action == "" {
		err = ui.Run(m)
	} else {
		// 确认阶段保留终端默认信号行为，Ctrl+C 能直接取消且尚未执行卸载。
		if action == "uninstall" {
			confirmed, confirmErr := confirmUninstall(input, out)
			if confirmErr != nil {
				fmt.Fprintln(errout, confirmErr)
				return 1
			}
			if !confirmed {
				fmt.Fprintln(out, "已取消卸载")
				return 0
			}
		}
		signals, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		ctx, timeout := context.WithTimeout(signals, 4*time.Minute)
		defer timeout()
		var message string
		switch action {
		case "install":
			err = m.InstallService(ctx)
			message = "服务已安装，内核保持停止；执行 tim start 启动"
		case "uninstall":
			err = m.UninstallService(ctx)
			message = "服务已卸载，内核已停止；配置、数据和断网保护保持原状"
		case "start":
			err = m.Start(ctx)
			message = "启动操作已完成"
		case "stop":
			err = m.Stop(ctx)
			message = "内核已停止"
		case "restart":
			message, err = m.Restart(ctx)
		case "update-sub":
			err = m.UpdateSubscription(ctx, hot)
			message = "订阅已更新；停止状态保持停止"
		case "status":
			var st core.Status
			st, err = m.Status(ctx)
			message = fmt.Sprintf("%s PID=%d", st.Message, st.PID)
			if st.Service.Installed {
				message += fmt.Sprintf(" 管理器=%s 服务=%s 状态=%s 开机启动=%t", st.Service.Manager, st.Service.Name, st.Service.State, st.Service.Enabled)
			} else {
				message += " 服务未安装"
			}
			if st.Guard.Enabled {
				message += fmt.Sprintf(" 断网保护=%t 后端=%s", st.Guard.Active, st.Guard.Backend)
			}
			if st.Guard.Error != "" {
				message += " 断网保护异常=" + st.Guard.Error
			}
		}
		if err == nil && message != "" {
			fmt.Fprintln(out, message)
		}
	}
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	return 0
}

func confirmUninstall(input io.Reader, out io.Writer) (bool, error) {
	if _, err := fmt.Fprint(out, "将停止内核并卸载服务，保留配置和数据。是否继续？[y/N] "); err != nil {
		return false, err
	}
	scanner := bufio.NewScanner(input)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, fmt.Errorf("读取卸载确认失败: %w", err)
		}
		return false, fmt.Errorf("未收到卸载确认，未执行卸载")
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes", nil
}

// 软链接入口和不同工作目录必须使用同一套配置与运行实例。
func executableConfigDir(executable string) (string, error) {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	return filepath.Dir(resolved), nil
}
