//go:build !linux

package core

func verifyRuntime(Process, *Document, RuntimeInfo) error { return supported() }
