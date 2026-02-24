//go:build windows

package claudeagent

import "os/exec"

// setProcGroup is a no-op on Windows.
// On Windows, exec.CommandContext handles child process cleanup.
func setProcGroup(cmd *exec.Cmd) {}

// killProcessGroup is a no-op on Windows.
func killProcessGroup(pid int) {}

// forceKillProcessGroup is a no-op on Windows.
func forceKillProcessGroup(pid int) {}
