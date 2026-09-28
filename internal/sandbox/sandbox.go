// Package sandbox applies a checkout's `[sandbox]` and routing declarations to
// the checkout itself — the verbs the sandbox image's run scripts call as
// `ticfac sandbox …`.
//
// They are a port of ticks' internal/sandbox as it stood at 7b6c0b2f^, the
// last commit before the chz cut made ticks tracker-only: the environment
// pre-flight, the model routing, the setup/toolchain/image tables, and the
// substrate decision, each reading `.tick/runners.toml` through this
// repository's own reader (internal/runconfig) rather than through tk. ticks
// keeps only the tracker's layout and `tk --json`; the runners format and
// these verbs are ticfac's, and the image's scripts call ticfac for them — the
// same move the cloud run family already made. What the port prints is held
// to what tk printed by internal/cli's sandbox_parity_test.go.
//
// The image-contract half of ticks' internal/sandbox (the constants and
// worker-boot strings the scripts and cloudflare/src/worker-boot.ts share)
// lives in internal/sandboximage, which owns the image tree itself; the
// worker-prompt template this package renders (workerprompt.go) appends
// sandboximage.WorkerPromptAddendum, so the two spellings of the container
// addendum cannot drift.
//
// SECURITY BOUNDARY, carried over verbatim: `setup` runs arbitrary shell
// inside a sandbox that holds the run's credentials, so its only source is the
// tracked, PR-reviewed config in the checkout — never a tick note, a model, a
// signal payload or an API parameter. There is deliberately no option on
// [SetupOptions] that supplies commands.
package sandbox

import (
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

// OrchestratorRole is the role the orchestrator resolves under in the
// role/tier table. There is deliberately no `[roles.orchestrator]` requirement
// behind it: an unknown role falls back to `implement` (runners-config.md),
// so a repository that has never thought about cloud runs still routes one.
const OrchestratorRole = "orchestrator"

// OrchestratorTier is the tier the orchestrator resolves at. The shared tier
// table calls the orchestrator frontier-class — it plans waves, reviews work
// and closes epics — so that is the cell asked for. A repository whose
// `implement` role defines no `frontier` variant simply keeps the role's own
// model, which is [runconfig.Config.Resolve]'s documented behaviour.
const OrchestratorTier = runconfig.TierFrontier

// WorkerRole is the role a per-tick worker container resolves under. It is
// `implement` because that is what a worker does — the frontier cell the
// orchestrator asks for plans waves and reviews epics, and routing every
// per-tick container at it is a silent multiple on every wave's bill.
const WorkerRole = "implement"

// DefaultCloudSubstrate is the substrate a cloud sandbox resolves when the
// control plane says nothing: harness dispatch. A container has no herdr
// server, so this is a deliberate choice, not a degradation, and the
// entrypoint announces it either way. It lives in sandboximage with the rest
// of the image contract; the alias keeps the substrate verb reading like the
// decision procedure it documents.
const DefaultCloudSubstrate = sandboximage.DefaultCloudSubstrate

// baseImageRef is the version-pinned base image reference. It is what a
// sandbox boots when the repository declares nothing — the same string
// [sandboximage.DefaultImage] derives from the Dockerfile's own pin, available
// to a caller that has a version but not this repository's checkout.
func baseImageRef(version string) string {
	return sandboximage.ImageName + ":" + version
}
