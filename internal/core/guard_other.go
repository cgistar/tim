//go:build !linux

package core

import (
	"syscall"
)

func guardPrivileges() error                                   { return supported() }
func guardSocketControl(string, string, syscall.RawConn) error { return supported() }
