//go:build !unix

package reconcile

import (
	"os"
	"syscall"
)

// gateProcessGroup is unix's process group, on a platform that has none. The
// timeout still bounds the wait (gate.go's WaitDelay); what it cannot promise
// here is that a grandchild dies with its parent.
func gateProcessGroup() *syscall.SysProcAttr { return nil }

func killGateGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}
