//go:build !linux

package update

import "syscall"

// childAttr has nothing to add outside Linux (no parent-death signal).
func childAttr() *syscall.SysProcAttr { return nil }
