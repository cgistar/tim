//go:build linux

package core

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("另一个管理操作正在执行: %w", err)
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
func processIdentity(pid int) (string, string, []string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", "", nil, err
	}
	end := strings.LastIndexByte(string(b), ')')
	if end < 0 {
		return "", "", nil, fmt.Errorf("无效 /proc stat")
	}
	fields := strings.Fields(string(b[end+1:]))
	if len(fields) < 20 {
		return "", "", nil, fmt.Errorf("不完整 /proc stat")
	}
	if fields[0] == "Z" {
		return "", "", nil, os.ErrNotExist
	}
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return "", "", nil, err
	}
	args, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return "", "", nil, err
	}
	return fields[19], strings.TrimSuffix(exe, " (deleted)"), strings.Split(strings.TrimRight(string(args), "\x00"), "\x00"), nil
}
func matchesProcess(p Process) (bool, error) {
	if p.PID <= 1 {
		return false, fmt.Errorf("无效 PID")
	}
	start, exe, args, err := processIdentity(p.PID)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if start != p.Start || filepath.Clean(exe) != filepath.Clean(p.Binary) {
		return false, nil
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-f" && args[i+1] == p.Runtime {
			return true, nil
		}
	}
	return false, nil
}
func signalManaged(p Process) error {
	// pidfd 将身份校验和发送信号绑定到同一内核进程，避免 PID 重用窗口。
	fd, err := unix.PidfdOpen(p.PID, 0)
	if err != nil {
		return fmt.Errorf("打开 pidfd（需要 Linux 5.3+）: %w", err)
	}
	defer unix.Close(fd)
	ok, err := matchesProcess(p)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("PID %s 身份已变化，拒绝发送信号", strconv.Itoa(p.PID))
	}
	return unix.PidfdSendSignal(fd, unix.SIGTERM, nil, 0)
}
func supported() error { return nil }
