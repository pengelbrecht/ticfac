//go:build unix

package gitbin

import (
	"os"
	"syscall"
)

// lockQueue takes a repository's queue lock, exclusively, and answers the
// function that lets go of it. An flock rather than a lock file, for the
// reason gatedir_unix.go gives: the kernel drops it with the last descriptor,
// so a ticfac killed while it held the lock leaves nothing behind to clear.
// The lock is held for a read and a write of a small file, never across a
// wait or a push.
func lockQueue(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

// tryLockQueue takes a lock without waiting: held is false when another
// holds it.
func tryLockQueue(path string) (release func(), held bool, err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EINTR {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, true, nil
}
