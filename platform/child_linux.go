package platform

import "syscall"

// childAttr makes the kernel kill helpers if Movie night dies.
func childAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
