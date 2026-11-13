package update

import "syscall"

// childAttr makes the kernel send the child SIGTERM when the launcher dies
// (even by SIGKILL), so no orphaned agent keeps running beside the next
// launcher. Pdeathsig fires when the forking OS thread exits; the runtime
// only ends a thread when a goroutine locked to it exits, which the launcher
// never does, so in practice that is the launcher process exiting.
func childAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
