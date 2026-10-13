//go:build !linux

package core

import (
	"fmt"
	"os/exec"
)

func supported() error {
	return fmt.Errorf("进程管理仅支持 Linux 5.3+；当前平台可执行配置测试和交叉构建")
}
func lockFile(string) (func(), error)                       { return nil, supported() }
func detach(*exec.Cmd)                                      {}
func processIdentity(int) (string, string, []string, error) { return "", "", nil, supported() }
func matchesProcess(Process) (bool, error)                  { return false, supported() }
func signalManaged(Process) error                           { return supported() }
