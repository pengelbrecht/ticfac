//go:build !unix

package gitbin

import "sync"

var queueLock sync.Mutex

// lockQueue without flock serialises this process's reservations only.
func lockQueue(string) (func(), error) {
	queueLock.Lock()
	return queueLock.Unlock, nil
}

var inflight = map[string]bool{}

// tryLockQueue without flock holds this process's pushes apart only.
func tryLockQueue(path string) (func(), bool, error) {
	queueLock.Lock()
	defer queueLock.Unlock()
	if inflight[path] {
		return nil, false, nil
	}
	inflight[path] = true
	return func() {
		queueLock.Lock()
		defer queueLock.Unlock()
		delete(inflight, path)
	}, true, nil
}
