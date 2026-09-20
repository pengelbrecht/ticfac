//go:build !unix

package reconcile

import "os"

// lockGateSlot on a platform with no flock. Nothing here keeps two gates out of
// one directory, so the honest answer is that there is no slot to take: the
// gate falls back to a throwaway worktree, which is slower and exactly what it
// did before tick 6wh. A shared gate directory would be worse than a slow one —
// it decides verdicts.
func lockGateSlot(string) (*os.File, error) { return nil, errNoGateSlotLock }
