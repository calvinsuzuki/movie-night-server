//go:build !windows && !linux

package platform

import "syscall"

func childAttr() *syscall.SysProcAttr { return nil }
