//go:build !linux

package agentbrowser

import "syscall"

// dieWithWorker is Linux-only (Pdeathsig); sandboxes are Linux.
func dieWithWorker(*syscall.SysProcAttr) {}
