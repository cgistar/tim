package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"tim/internal/core"
)

func runGuard(args []string, input io.Reader, out, errout io.Writer) int {
	valid := len(args) == 1 && (args[0] == "status" || args[0] == "on" || args[0] == "off") || len(args) == 2 && args[0] == "off" && args[1] == "--yes"
	if !valid {
		fmt.Fprint(errout, usage)
		return 2
	}
	if args[0] == "off" && len(args) == 1 {
		fmt.Fprint(out, "关闭保护并恢复直连？认证后需手动重新开启。[y/N] ")
		scanner := bufio.NewScanner(input)
		if !scanner.Scan() {
			fmt.Fprintln(errout, "未收到关闭确认，保护保持原状")
			return 1
		}
		answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(out, "已取消关闭保护")
			return 0
		}
	}
	signals, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, timeout := context.WithTimeout(signals, 4*time.Minute)
	defer timeout()
	var err error
	switch args[0] {
	case "off":
		err = core.DisableGlobalNetworkGuard(ctx)
		if err == nil {
			fmt.Fprintln(out, "断网保护已关闭，已恢复直连")
		}
	case "status":
		var st core.GuardStatus
		st, err = core.NetworkGuardState(ctx)
		if !st.Enabled && err == nil {
			fmt.Fprintln(out, "断网保护未开启")
		} else {
			fmt.Fprintf(out, "断网保护 规则存在=%t 规则生效=%t 后端=%s\n", st.Enabled, st.Active, st.Backend)
		}
	case "on":
		var executable, dir string
		executable, err = os.Executable()
		if err == nil {
			dir, err = executableConfigDir(executable)
		}
		if err == nil {
			var store *core.Store
			store, err = core.Open(dir)
			if err == nil {
				err = core.NewManager(store).EnableNetworkGuard(ctx)
			}
		}
		if err == nil {
			fmt.Fprintln(out, "断网保护已开启；TUN 未运行时普通应用公网直连被阻止")
		}
	}
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	return 0
}
