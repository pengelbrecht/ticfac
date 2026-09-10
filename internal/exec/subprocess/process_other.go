//go:build !unix

package subprocess

import (
	"fmt"
	"syscall"
)

// This executor is a unix subprocess host: it needs process groups to kill a
// tree and to survive its own caller. Rather than pretend on a platform that
// has neither, it refuses — a cancel that silently kills one pid out of a tree
// is the failure mode Appendix A #1 is about.

func newProcessGroup() *syscall.SysProcAttr { return nil }

func processAlive(pid int) bool { return false }

func signalGroup(pgid int, sig syscall.Signal) error {
	return fmt.Errorf("the local subprocess executor needs unix process groups")
}

func sigTerm() syscall.Signal { return syscall.Signal(15) }
func sigKill() syscall.Signal { return syscall.Signal(9) }
