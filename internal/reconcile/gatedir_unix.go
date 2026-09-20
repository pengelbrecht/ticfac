//go:build unix

package reconcile

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockGateSlot takes one gate slot, exclusively and without waiting, and holds
// it for as long as the returned file is open.
//
// An flock rather than a lock FILE, for the reason tick rmc gave when this
// repository last decided who owns something: a lock file with a pid in it is
// only as good as the pid, and on a busy macOS host pids were measured
// advancing ~700 a second through a space that wraps at 99999, so a dead gate's
// pid is somebody else's within minutes. A flock is held by the open file
// DESCRIPTION, and the kernel drops it when the last descriptor closes — which
// a process that was SIGKILLed cannot avoid and a process that inherited its
// number cannot fake. There is no stale lock to reclaim and no liveness check
// to get wrong.
//
// The one imprecision, measured while writing the test for this: a lock can
// read as held for a few microseconds after it was released, because a process
// forked between fork and exec carries a copy of every descriptor its parent
// had — the lock's included — and this reconciler forks constantly. It is
// one-directional, which is what makes it safe: the window can only make a FREE
// slot look busy, never a busy one look free. The cost is that a gate
// occasionally takes the next slot, or occasionally falls back to a throwaway,
// and pays for a cold run it did not have to. It never shares a directory.
//
// Non-blocking on purpose: a gate that waited here would be serialising gates
// on the host, which is a decision tick w1j owns and not a side effect this one
// gets to have. A busy slot means "try the next one", and no free slot means a
// throwaway worktree — the cold, correct behaviour every gate had before.
func lockGateSlot(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errGateSlotBusy
		}
		// Not "busy": a filesystem that does not implement flock (some network
		// mounts answer ENOTSUP or EINVAL) would answer the same for every
		// slot, and trying three more of them would only be slower about
		// reaching the same fallback.
		return nil, fmt.Errorf("%w: %v", errNoGateSlotLock, err)
	}
	return file, nil
}
