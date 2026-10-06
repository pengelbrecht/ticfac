//go:build !linux

package subprocess

// processZombie on a platform with no /proc to read: no answer, and none
// needed. This package's non-Linux unix host is macOS, whose PID 1 (launchd)
// reaps orphans in milliseconds — the window in which a killed child is
// observable as a zombie never stays open there, so signal 0 alone separates
// the living from the dead. The factory container is Linux, and that is where
// the window never closes; the real answer lives in zombie_linux.go.
func processZombie(pid int) bool { return false }

// groupZombie is processZombie for a whole process group, and says no with
// it: with no /proc there is no member to enumerate, and nothing a group
// signal left behind to mistake for one.
func groupZombie(pgid int) bool { return false }
