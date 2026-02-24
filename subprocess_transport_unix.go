//go:build !windows

package claudeagent

import (
	"os/exec"
	"syscall"
)

// setProcGroup configures the command to start in its own session.
// Setsid creates a new session and process group, detaching from the
// controlling terminal. This prevents SIGTTIN/SIGTTOU stops when the
// child (or Node.js libraries it loads) tries to access /dev/tty.
// All child processes (including grandchildren spawned by Claude's Task tool)
// belong to the same group and can be killed together.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// killProcessGroup sends SIGTERM to the entire process group.
func killProcessGroup(pid int) {
	syscall.Kill(-pid, syscall.SIGTERM)
}

// forceKillProcessGroup sends SIGKILL to the entire process group.
func forceKillProcessGroup(pid int) {
	syscall.Kill(-pid, syscall.SIGKILL)
}
