//go:build !darwin && !linux

package subprocess

import "errors"

// waitExitedUnreaped has no implementation here: the supervisor then reaps
// its runner as soon as it exits, and a stop signals the runner's group only
// until then (see exitwait.go).
func waitExitedUnreaped(pid int) error {
	return errExitPeekUnsupported
}

var errExitPeekUnsupported = errors.New("this platform cannot observe a child's exit without reaping it")
