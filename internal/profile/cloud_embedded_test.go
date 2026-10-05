package profile

import "testing"

// short: resolving the embedded cloud set reads the bytes compiled into the
// binary and touches no repository and no subprocess.

// The cloud profile set travels inside the executable, and `ticfac run
// --cloud-workers` resolves it by its virtual name: a laptop driving cloud
// workers dispatches every role through the cloudflare-sandbox executor with
// the same bytes the orchestrator image installs, never a path on disk.
func TestTheEmbeddedCloudSetResolvesEveryRoleToTheSandboxDoor(t *testing.T) {
	for _, role := range append(append([]string{}, Roles...), RoleResolveConflict, RoleRepairGate) {
		resolved, err := Resolve(role, Options{Dir: EmbeddedCloud})
		if err != nil {
			t.Fatalf("resolve %s from the embedded cloud set: %v", role, err)
		}
		if resolved.Executor != "cloudflare-sandbox" {
			t.Errorf("%s resolved executor %q, want cloudflare-sandbox", role, resolved.Executor)
		}
		if resolved.Runner != "pi-durable" {
			t.Errorf("%s resolved runner %q from the embedded cloud set, want pi-durable: the set "+
				"names the hosted harness the factory's WorkerAgent runs the conversation on "+
				"(epic 43y, tick qf4), and the bytes inside the binary are the ones the "+
				"image stages", role, resolved.Runner)
		}
		if resolved.Runner == "claude" {
			t.Errorf("%s resolved the claude runner from the cloud set: nothing in the cloud may run claude", role)
		}
		if want := CloudFSRoot + "/" + role + ".json"; resolved.Source != want {
			t.Errorf("%s's provenance names %q, want the embedded set's %q", role, resolved.Source, want)
		}
	}
}
