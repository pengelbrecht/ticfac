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
