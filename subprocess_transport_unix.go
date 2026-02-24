//go:build !windows

package claudeagent

import (
	"os/exec"
	"syscall"
)

// setProcGroup configures the command to start in its own process group.
// This ensures all child processes (including grandchildren spawned by Claude's Task tool)
// belong to the same group and can be killed together.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGTERM to the entire process group.
func killProcessGroup(pid int) {
	syscall.Kill(-pid, syscall.SIGTERM)
}

// forceKillProcessGroup sends SIGKILL to the entire process group.
func forceKillProcessGroup(pid int) {
	syscall.Kill(-pid, syscall.SIGKILL)
}
