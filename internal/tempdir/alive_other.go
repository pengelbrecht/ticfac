//go:build !unix

package tempdir

// processAlive cannot ask on this platform, so it answers alive: Sweep keeps
// what it cannot decide about.
func processAlive(pid int) bool { return true }
