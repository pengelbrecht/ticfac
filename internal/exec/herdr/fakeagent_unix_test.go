//go:build unix

package herdr

import "syscall"

// fakeAgentProcAttr starts the fake agent as the leader of its own process
// group, so one signal reaches it and everything it runs.
func fakeAgentProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// signalFakeAgentGroup sends SIGTERM (or SIGKILL when kill is set) to the
// fake agent's whole process group — by its exact group id, never a pattern.
func signalFakeAgentGroup(pgid int, kill bool) {
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(-pgid, sig)
}

// fakeAgentGroupAlive answers whether any process is still in the group.
func fakeAgentGroupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || err == syscall.EPERM
}
