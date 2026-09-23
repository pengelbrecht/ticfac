//go:build !linux

package reconcile

// processZombie on a platform with no /proc to read: no answer, and none
// needed. processAlive's non-Linux unix callers are macOS and the BSDs, whose
// PID 1 (launchd, on the Mac) reaps orphans in milliseconds — the window in
// which a killed child is observable as a zombie never stays open there, and
// a test polling for liveness distinguishes dead from live through the reap.
// The factory container is Linux, and that is where the window never closes;
// the real answer lives in zombie_linux_test.go.
func processZombie(pid int) bool { return false }
