//go:build linux

package core

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func guardPrivileges() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("断网保护需要 sudo/root")
	}
	return supported()
}

func guardSocketControl(_, _ string, raw syscall.RawConn) error {
	var socketErr error
	err := raw.Control(func(fd uintptr) { socketErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, guardTimMark) })
	if err != nil {
		return err
	}
	return socketErr
}
