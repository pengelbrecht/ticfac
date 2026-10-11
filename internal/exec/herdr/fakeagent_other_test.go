//go:build !unix

package herdr

import (
	"os"
	"syscall"
)

// The fake agent is a /bin/sh script: off unix there are no process groups
// to manage, so these only keep the package compiling.

func fakeAgentProcAttr() *syscall.SysProcAttr { return nil }

func signalFakeAgentGroup(pid int, _ bool) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

func fakeAgentGroupAlive(int) bool { return false }
