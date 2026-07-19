//go:build unix

package hook

import (
	"os/exec"
	"syscall"
)

// detach puts the spawned worker in its own session so it survives the hook
// process group being torn down when Claude Code reaps the hook.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
