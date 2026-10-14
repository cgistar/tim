package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"tim/internal/core"
	"tim/internal/ui"
)

const usage = `用法: tim [start|stop|restart|status|update-sub [--hot]]
无参数打开 TUI。
配置固定为 tim 实际可执行文件所在目录的 setting.yaml；DATA 优先于 setting.yaml 的 data-dir。
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
func run(args []string, out, errout io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(out, usage)
		return 0
	}
	action := ""
	hot := false
	if len(args) > 0 {
		action = args[0]
		switch action {
		case "start", "stop", "restart", "status":
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
		signals, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		ctx, timeout := context.WithTimeout(signals, 4*time.Minute)
		defer timeout()
		var message string
		switch action {
		case "start":
			err = m.Start(ctx)
			message = "内核正在运行"
		case "stop":
			err = m.Stop(ctx)
			message = "内核已停止"
		case "restart":
			message, err = m.Restart(ctx)
		case "update-sub":
			err = m.UpdateSubscription(ctx, hot)
			message = "订阅已更新并应用"
		case "status":
			var st core.Status
			st, err = m.Status(ctx)
			message = fmt.Sprintf("%s PID=%d", st.Message, st.PID)
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

// 软链接入口和不同工作目录必须使用同一套配置与运行实例。
func executableConfigDir(executable string) (string, error) {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	return filepath.Dir(resolved), nil
}
