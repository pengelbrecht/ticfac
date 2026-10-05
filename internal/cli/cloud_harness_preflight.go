package cli

// The harness preflight of a cloud submission (epic 43y, tick kkt).
//
// A cloud run's containers boot the FACTORY's image, never the pushed
// branch's: the image carries the harnesses and the entrypoints, and the
// branch carries only the work. So the one fact that decides whether a
// submitted run can boot its first worker is a fact about the FACTORY —
// which harness kinds its image ships — checked against a fact about the
// BRANCH — which kind each of its cloud-routed jobs resolves to. When they
// disagree, every container of the run dies at boot, one wave at a time,
// with "unknown harness kind" (tick twa's finding: a cloud dispatch of this
// repository on the new image, pre-flip overlay). The runbook for [A4]'s
// cloud half (docs/pi-durable-cloud-runbook.md) orders the steps that keep
// them in agreement; this preflight is the order made mechanical — the
// refusal happens at the operator's knee, before anything is pushed,
// booted or paid for.
//
// What each half is read from, and why it is the authority it is:
//
//   - The branch half is HEAD's `.tick/runners.toml` (+ the
//     `.tick/runners.cloud.toml` overlay merged over it), extracted with
//     git — HEAD, not the working tree, because HEAD is the exact boundary
//     prepareCloudSubmission pushes — and resolved with the SAME questions
//     `ticfac doctor` and a run's own start ask (reconcile.CheckRouting on
//     the embedded cloud profile set): every role, every tier the ladder
//     can reach, and the on-demand jobs at the ceiling. A repo whose
//     routing does not resolve is refused here too, for the same reason a
//     run refuses at start rather than stopping at its first merge conflict
//     — only earlier, and cheaper.
//
//   - The factory half is the factory's own answer (GET /api/deployment,
//     harness_kinds): the kinds its image accepts, pinned to
//     image/common.sh's kind case by a parity test. A factory that predates
//     the field — or cannot be asked — reports nothing, and a missing
//     answer is a WARNING, never "shipped nothing": refusing every
//     submission to an older factory would break the everyday command for
//     every repository at once, and a warning names exactly what to do
//     (`ticfac factory wait-deployed`) without pretending to knowledge
//     nobody has.
//
// Where it runs: inside submitCloudRun, before prepareCloudSubmission, so
// it covers both surfaces that submit through it — `ticfac run <epic>
// --cloud` (the everyday command, [A4]'s own runbook step) and `run
// --cloud-workers` (whose workers boot factory containers too) — and so a
// refusal costs nothing. The expert `ticfac cloud run` submits without it:
// the expert verbs are the operator's own escape hatch, same as their other
// flags. A repo with no `.tick/runners.toml` at HEAD routes on the
// factory's defaults, so the preflight has no branch half to check and
// stays silent.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/factory"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// preflightCloudHarness refuses a cloud submission whose factory and branch
// disagree about the harnesses a container can run. nil means the submission
// may proceed — checked, or honestly unchecked with the warning written to
// prose naming why. The error names every offending job and the runbook's
// order for fixing either half.
func preflightCloudHarness(ctx context.Context, client *cloudClient, repo string, prose io.Writer) error {
	// The branch half. HEAD is the pushed boundary; a repo that declares no
	// runners config there routes on the factory's defaults and is not this
	// preflight's to check.
	common, err := cloudGit(ctx, repo, "show", "HEAD:"+runconfig.FileName)
	if err != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "ticfac-harness-preflight-")
	if err != nil {
		// A temp directory is not a fact about the submission; the run's
		// own routing checks (doctor, start) still guard it.
		fmt.Fprintf(prose, "the harness preflight cannot run: %v\n", err)
		return nil
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "runners.toml"), []byte(common), 0o600); err != nil {
		return nil
	}
	// The cloud overlay, from HEAD too: LoadFor reads it beside the common
	// file by its own name, and its absence is itself an answer (every cloud
	// role is then refused as unrouted, which the check below reports).
	if overlay, err := cloudGit(ctx, repo, "show", "HEAD:"+runconfig.OverrideFileName(runconfig.SubstrateCloud)); err == nil {
		if err := os.WriteFile(filepath.Join(dir, "runners.cloud.toml"), []byte(overlay), 0o600); err != nil {
			return nil
		}
	}

	// Every job a cloud run of this branch can dispatch, routed exactly the
	// way its containers will be. A config defect is refused here rather
	// than at run start — the same refusal, at the cheaper moment.
	jobs, err := reconcile.CheckRouting(profile.EmbeddedCloud, filepath.Join(dir, "runners.toml"), runconfig.SubstrateCloud)
	if err != nil {
		return fmt.Errorf("the cloud routing at HEAD does not resolve: %w — a run would refuse to start on the same question; fix the cell the error names", err)
	}

	// The factory half: its own answer to what its image ships.
	facts, err := factory.FetchDeployed(ctx, client.http, client.baseURL, client.token)
	if err != nil {
		fmt.Fprintf(prose, "the harness preflight cannot run: the factory does not answer what its image ships (%v) — "+
			"proceeding without it; `ticfac factory status` names what the factory runs\n", err)
		return nil
	}
	if len(facts.HarnessKinds) == 0 {
		fmt.Fprintf(prose, "the factory%s does not report the harness kinds its image ships (an older deployment) — "+
			"proceeding without the harness preflight; `ticfac factory wait-deployed <merge sha>` is how you wait for one that does\n",
			deployedAt(facts))
		return nil
	}
	shipped := make(map[string]bool, len(facts.HarnessKinds))
	for _, kind := range facts.HarnessKinds {
		shipped[strings.TrimSpace(kind)] = true
	}

	var unshipped []string
	for _, job := range jobs {
		kind := job.Profile.Runner
		if shipped[kind] {
			continue
		}
		at := job.Role
		if job.Tier != "" {
			at = fmt.Sprintf("%s (tier %s)", job.Role, job.Tier)
		}
		unshipped = append(unshipped, fmt.Sprintf("%s: %s", at, kind))
	}
	if len(unshipped) == 0 {
		return nil
	}
	return fmt.Errorf(
		"the cloud routing at HEAD names harness kinds this factory%s does not ship — %s:\n  %s\n"+
			"every container of a run started now would die at boot with \"unknown harness kind\". "+
			"The runbook's order (docs/pi-durable-cloud-runbook.md): flip the .tick/runners.cloud.toml cells to a kind the image ships (%s), "+
			"or wait for the factory to carry the kind's support with `ticfac factory wait-deployed <merge sha>`",
		deployedAt(facts), pluralJobs(len(unshipped)), strings.Join(unshipped, "\n  "),
		strings.Join(facts.HarnessKinds, ", "),
	)
}

// deployedAt names the factory's own version when it has one, so a refusal
// says WHICH factory refused — the deployment the operator must move.
func deployedAt(facts *factory.DeployedFacts) string {
	if facts == nil || facts.Version == "" {
		return ""
	}
	return " at " + facts.Version
}

// pluralJobs is the one word that changes with the list's length.
func pluralJobs(n int) string {
	if n == 1 {
		return "1 job routes to a kind it does not ship"
	}
	return fmt.Sprintf("%d jobs route to kinds it does not ship", n)
}
