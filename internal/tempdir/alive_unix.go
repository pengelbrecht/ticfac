//go:build unix

package tempdir

import "syscall"

// processAlive asks the kernel. A reused pid reads alive, which only keeps a
// directory Sweep could have removed.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
