package factory

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The cloud profile set in the orchestrator image (tick gbs).
//
// The staged orchestrator entrypoint (ticfacentrypoint.go) execs
// `ticfac run-epic --profiles /usr/local/share/ticfac/profiles-cloudflare-sandbox`,
// so the image has to carry that directory — and it has to be the CLOUD set,
// because the profiles compiled into the binary as the default are the LOCAL
// ones: `profiles/`, whose executor is local-subprocess. A container whose
// run-epic resolved the default dispatched every worker of the epic as a
// subprocess of the orchestrator's own container — which is exactly how the
// 2026-09-23 smoke tick ran, and what this tick exists to stop.
//
// WHY THE SET IS STAGED FROM THE BINARY'S EMBEDDED COPY rather than read off
// the deploying checkout's disk. For the same reason the binaries are
// cross-compiled rather than `go install`ed (ticfacbin.go), plus the reason
// profiles are embedded at all (embedded.go): the deploy stages the image
// context, and the profiles a container resolves are provenance — every
// attempt record cites them — so they must not be able to disagree with the
// ticfac that resolves them. Staging from the copy embedded in the deploying
// binary gives that by construction: same commit, same bytes, and the
// vendored image/ tree is still never edited (the block below is inserted
// into the STAGED Dockerfile, like the binaries' install block).

// The payload seam: wired from the module-root embed in payload.go, so the
// tests can stage a fake set through the same code path the deploy runs.
var cloudProfilesFS fs.FS

// cloudProfilesContextName is the set's directory as staged into the image
// build context, at the context root beside the staged binaries — named as
// this repository carries it, so a reader of the staged context sees what the
// directory is without a comment.
const cloudProfilesContextName = "profiles-cloudflare-sandbox"

// cloudProfilesContainerPath is where the image installs the set, and the path
// the staged orchestrator entrypoint passes to `ticfac run-epic --profiles`:
// one place in this repository spells the contract, and the entrypoint block
// and the Dockerfile block both read it.
const cloudProfilesContainerPath = "/usr/local/share/ticfac/" + cloudProfilesContextName

// cloudProfilesMarker identifies the inserted Dockerfile block, so a second
// application is a refusal rather than a duplicated COPY.
const cloudProfilesMarker = "# >>> cloud profile set (tick gbs)"

// StageCloudProfiles puts the cloud profile set into the staged image
// context: the embedded files written under profiles-cloudflare-sandbox/ at
// the context root, and the staged Dockerfile taught to install them at
// cloudProfilesContainerPath.
//
// Same contract as the staged rewrites around it (SetSandboxTkPins,
// SetSandboxTicfacPins, SetSandboxOrchestratorEntrypoint): the staged copy is
// edited, the vendored image/ tree is not, and this must run AFTER
// MaterializeSandbox — which rewrites the staged Dockerfile and prunes
// everything under the context the embedded tree does not own, so staging
// before it would stage into a directory about to be swept.
func StageCloudProfiles(dir string) error {
	if cloudProfilesFS == nil {
		return missingPayload("cloud profile set")
	}
	dest := filepath.Join(dir, cloudProfilesContextName)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("preparing %s: %w", dest, err)
	}
	err := fs.WalkDir(cloudProfilesFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(cloudProfilesFS, p)
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(p, cloudProfilesContextName+"/")
		if err := os.WriteFile(filepath.Join(dest, filepath.FromSlash(name)), data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", filepath.Join(cloudProfilesContextName, name), err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("staging the cloud profile set into the image context: %w", err)
	}

	configPath := filepath.Join(dir, sandboxDockerfileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", configPath, err)
	}
	if strings.Contains(string(data), cloudProfilesMarker) {
		return fmt.Errorf("%s already carries the cloud profile set — the staged Dockerfile is written fresh by MaterializeSandbox on every deploy, so a second insertion means the staging order is wrong", configPath)
	}
	var b strings.Builder
	b.Write(data)
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	b.WriteString(cloudProfilesInstallBlock)
	if err := os.WriteFile(configPath, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", configPath, err)
	}
	return nil
}

// cloudProfilesInstallBlock is appended to the staged Dockerfile, after the
// ticfac install block, so the deploy's image edits read in the order the
// deploy makes them. Last on purpose, for the same reason the ticfac block
// is: everything above it is identical across deploys of the same vendored
// tree, and this layer changes only when the profile set does.
//
// Not `go install`able from a module path and not upstreamable into ticks'
// own Dockerfile, for the same reasons the ticfac binaries are not
// (ticfacbin.go): ticks' build context holds no cloud profiles — the cloud
// set is ticfac's — and the vendored tree may not be edited here.
const cloudProfilesInstallBlock = cloudProfilesMarker + `
#
# The CLOUD PROFILE SET (tick gbs): profiles-cloudflare-sandbox/, staged into
# this build context from the copy embedded in the deploying ticfac binary —
# the same commit as the binaries above, by the same construction — and
# installed at the path the staged orchestrator entrypoint passes to
# ` + "`ticfac run-epic --profiles`" + `. The profiles compiled into ticfac as the
# default are the LOCAL set: their executor is local-subprocess, which would
# run every worker of this run as a process inside THIS container. The cloud
# set pairs every role with executor cloudflare-sandbox, so each worker boots
# in its own container through this run's factory.
COPY ` + cloudProfilesContextName + ` ` + cloudProfilesContainerPath + `
# Green-start discipline, as for the binaries above: a role profile the image
# does not carry is a run that dies at reconciler construction, after the
# clone and the pre-flight are paid for — so the build refuses here instead.
RUN set -euo pipefail; \
    for role in implement-tick review-epic closeout-epic; do \
      test -r "` + cloudProfilesContainerPath + `/${role}.json" && \
      test -r "` + cloudProfilesContainerPath + `/${role}.md" || { \
        echo "the cloud profile set at ` + cloudProfilesContainerPath + ` is missing ${role}" >&2; \
        exit 1; }; \
    done
# <<< cloud profile set (tick gbs)
`
