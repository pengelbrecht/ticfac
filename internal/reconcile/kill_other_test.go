//go:build !unix

package reconcile

import "os"

func syscallKillGroup(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

// processAlive is the same question this platform can answer: FindProcess fails
// for a pid that does not exist.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.FindProcess(pid)
	return err == nil
}
