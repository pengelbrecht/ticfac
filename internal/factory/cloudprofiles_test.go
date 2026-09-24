package factory

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	ticfac "github.com/pengelbrecht/ticfac"
	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/exec/cloudflaresandbox"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// The cloud profile set in the image the container boots (tick gbs).
//
// The container's run-epic resolves profiles-cloudflare-sandbox/ — the
// directory the staged orchestrator entrypoint points `--profiles` at — and
// the image has to CARRY that directory for the resolution to have something
// to read. The compiled-in profiles/ are the LOCAL set: their executor is
// local-subprocess, and a run-epic that resolved them would dispatch every
// worker of the epic as a subprocess inside the orchestrator's own container —
// which is exactly how the 2026-09-23 smoke tick ran, and what this tick
// exists to stop. The staging follows the binaries' pattern (ticfacbin.go):
// the set is embedded in the deploying binary, staged into the image build
// context from that copy, and installed at the fixed path the entrypoint
// names — so the profiles the container resolves and the binary it runs are
// the same commit by construction, not by coincidence.

// short: writes the embedded set and one Dockerfile into temp dirs and reads
// them back; no compiler, no Docker, no network.
func TestStageCloudProfilesPutsTheSetInTheImageContext(t *testing.T) {
	dir := t.TempDir()
	if err := MaterializeSandbox(dir); err != nil {
		t.Fatalf("MaterializeSandbox: %v", err)
	}
	if err := StageCloudProfiles(dir); err != nil {
		t.Fatalf("StageCloudProfiles: %v", err)
	}

	// The staged files are the embedded set, byte for byte, and nothing else.
	embedded := map[string][]byte{}
	err := fs.WalkDir(ticfac.CloudProfiles(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(ticfac.CloudProfiles(), p)
		if err != nil {
			return err
		}
		embedded[strings.TrimPrefix(p, cloudProfilesContextName+"/")] = data
		return nil
	})
	if err != nil {
		t.Fatalf("reading the embedded cloud profile set: %v", err)
	}
	if len(embedded) == 0 {
		t.Fatal("this build embeds no cloud profile set: the container's run-epic has nothing to resolve")
	}
	staged := map[string][]byte{}
	root := filepath.Join(dir, cloudProfilesContextName)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		staged[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		t.Fatalf("walking the staged cloud profile set: %v", err)
	}
	if len(staged) != len(embedded) {
		names := func(m map[string][]byte) string {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return strings.Join(keys, ", ")
		}
		t.Fatalf("the staged set is %d files, the embedded set is %d:\nstaged:   %s\nembedded: %s",
			len(staged), len(embedded), names(staged), names(embedded))
	}
	for name, want := range embedded {
		if string(staged[name]) != string(want) {
			t.Errorf("%s staged is not the embedded copy: the profiles the container resolves must be the ones this binary carries", name)
		}
	}

	// And the Dockerfile installs them at the path the entrypoint names,
	// refusing the build when a role profile is missing (green-start
	// discipline, as for the binaries).
	data, err := os.ReadFile(filepath.Join(dir, sandboxDockerfileName))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"COPY " + cloudProfilesContextName + " " + cloudProfilesContainerPath,
		// the green-start assertion: every role profile readable in the image,
		// or the build fails rather than the run.
		`test -r "` + cloudProfilesContainerPath + `/${role}.json"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the staged Dockerfile does not carry %q:\n%s", want, got)
		}
	}
}

// short: one temp dir.
//
// MaterializeSandbox rewrites the staged Dockerfile from the embedded tree on
// every deploy and prunes everything under the context it does not own, so
// staging the profiles BEFORE it would stage into a directory about to be
// swept — the same ordering the binaries are refused for. The refusal here is
// the Dockerfile read: a context with no Dockerfile is a context that was
// never materialized.
func TestStageCloudProfilesRefusesAContextThatWasNeverMaterialized(t *testing.T) {
	err := StageCloudProfiles(t.TempDir())
	if err == nil {
		t.Fatal("the cloud profile set was staged into a context with no Dockerfile — before MaterializeSandbox, which would prune it again")
	}
}

// short: one temp dir.
//
// The staged Dockerfile is written fresh by MaterializeSandbox on every
// deploy, so a second insertion means the staging order is wrong — the same
// guard every other staged rewrite carries.
func TestStageCloudProfilesRefusesASecondApplication(t *testing.T) {
	dir := t.TempDir()
	if err := MaterializeSandbox(dir); err != nil {
		t.Fatalf("MaterializeSandbox: %v", err)
	}
	if err := StageCloudProfiles(dir); err != nil {
		t.Fatalf("first application: %v", err)
	}
	err := StageCloudProfiles(dir)
	if err == nil {
		t.Fatal("a second application was accepted; the staged Dockerfile would carry the cloud profile set twice")
	}
	if !strings.Contains(err.Error(), "already carries the cloud profile set") {
		t.Errorf("refusal does not say what happened: %v", err)
	}
}

// The tick's second acceptance item: NO ROLE of a cloud run resolves to
// executor local-subprocess — the executor that runs a worker inside the
// orchestrator's own container. Proved on the FINAL resolved value, after
// every overlay (the xte close-out learning: a policy checked on one input
// layer passes while the whole fails), resolving the way the container's
// run-epic does: the STAGED set (the bytes the image carries), the target
// repository's own runners.toml with its cloud overlay, the cloud substrate.
//
// short: reads of the staged set and this repository's own runners files; no
// I/O beyond those.
func TestNoRoleOfACloudRunResolvesToTheLocalSubprocessExecutor(t *testing.T) {
	dir := t.TempDir()
	if err := MaterializeSandbox(dir); err != nil {
		t.Fatalf("MaterializeSandbox: %v", err)
	}
	if err := StageCloudProfiles(dir); err != nil {
		t.Fatalf("StageCloudProfiles: %v", err)
	}
	staged := filepath.Join(dir, cloudProfilesContextName)

	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(root, ".tick", "runners.toml")

	// Every role, and every tier the roles table can route a role to — the
	// balanced tier among them, whose common-file cell routes codex and whose
	// cloud overlay is what must win.
	cases := []struct{ role, tier string }{
		{"implement-tick", ""},
		{"implement-tick", "economy"},
		{"implement-tick", "strong"},
		{"implement-tick", "balanced"},
		{"review-epic", ""},
		{"closeout-epic", ""},
	}
	for _, c := range cases {
		p, err := profile.Resolve(c.role, profile.Options{
			Dir: staged, RunnersConfig: gate, Tier: c.tier, Substrate: string(runconfig.SubstrateCloud),
		})
		if err != nil {
			t.Fatalf("%s (tier %q) did not resolve from the staged cloud set under the cloud substrate: %v", c.role, c.tier, err)
		}
		if p.Executor == subprocess.ExecutorName {
			t.Errorf("%s (tier %q) resolved to executor %s: the LOCAL set, which runs the worker as a subprocess of the orchestrator's own container — a cloud run must resolve the cloud set (tick gbs)",
				c.role, c.tier, subprocess.ExecutorName)
			continue
		}
		if p.Executor != cloudflaresandbox.ExecutorName {
			t.Errorf("%s (tier %q) resolved to executor %q, want %s: the cloud set pairs every role with the executor that boots one worker container per attempt through the factory's door",
				c.role, c.tier, p.Executor, cloudflaresandbox.ExecutorName)
		}
		if p.Runner != "pi" {
			t.Errorf("%s (tier %q) resolved to runner %q, want pi: the cloud's workers run GLM through pi, and a role that resolved another harness after every overlay would be one the overlays moved",
				c.role, c.tier, p.Runner)
		}
	}

	// The contrast that makes the entrypoint's `--profiles` line necessary:
	// the compiled-in set IS the local one. A container whose run-epic
	// resolved the default — which is what the staged entrypoint did before
	// this tick — would name local-subprocess for every role it dispatched,
	// routing or no routing, because routing moves the runner and the model
	// and never the executor.
	compiledIn, err := profile.Resolve("implement-tick", profile.Options{
		RunnersConfig: gate, Substrate: string(runconfig.SubstrateCloud),
	})
	if err != nil {
		t.Fatalf("the compiled-in set did not resolve under the cloud substrate: %v", err)
	}
	if compiledIn.Executor != subprocess.ExecutorName {
		t.Errorf("the compiled-in set names executor %q, not %s: the local default is what makes --profiles load-bearing in the container, and a reader of the entrypoint needs this contrast to see why",
			compiledIn.Executor, subprocess.ExecutorName)
	}
}
