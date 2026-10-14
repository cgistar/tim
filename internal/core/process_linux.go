//go:build linux

package core

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func lockFile(path string) (func(), error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
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

// 精确匹配私有运行配置和工作目录；每次发现后仍用启动时间与 pidfd 防止 PID 复用。
func discoverProcess(config, home string) (Process, bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return Process{}, false, err
	}
	var found Process
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		raw, err := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			continue
		}
		if err != nil {
			return Process{}, false, err
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if !processRuntimeArgs(pid, args, config, home) {
			continue
		}
		start, binary, args, err := processIdentity(pid)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return Process{}, false, fmt.Errorf("核验目标进程 %d: %w", pid, err)
		}
		if !processRuntimeArgs(pid, args, config, home) {
			continue
		}
		if found.PID != 0 {
			return Process{}, false, fmt.Errorf("同一运行配置存在多个进程 (%d, %d)，拒绝操作", found.PID, pid)
		}
		found = Process{PID: pid, Start: start, Binary: binary, Runtime: config}
	}
	return found, found.PID != 0, nil
}

func runtimeArgs(args []string, config, home string) bool {
	var runtimePath, directory string
	for i := 1; i < len(args); i++ {
		if args[i] == "-t" {
			return false
		}
		if i+1 < len(args) {
			switch args[i] {
			case "-f":
				runtimePath = args[i+1]
				i++
			case "-d":
				directory = args[i+1]
				i++
			}
		}
	}
	return runtimePath == config && directory == home
}
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
		if args[i] == "-f" && processPath(p.PID, args[i+1]) == p.Runtime {
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
func supported() error {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return fmt.Errorf("当前只支持 amd64/arm64")
	}
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return fmt.Errorf("需要可用 pidfd 的 Linux 5.3+ 环境: %w", err)
	}
	return unix.Close(fd)
}
func processPath(pid int, path string) string {
	if !filepath.IsAbs(path) {
		cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
		if err != nil {
			return ""
		}
		path = filepath.Join(cwd, path)
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}
func processRuntimeArgs(pid int, args []string, config, home string) bool {
	copied := append([]string(nil), args...)
	for i := 1; i+1 < len(copied); i++ {
		if copied[i] == "-f" || copied[i] == "-d" {
			copied[i+1] = processPath(pid, copied[i+1])
			i++
		}
	}
	return runtimeArgs(copied, config, home)
}
