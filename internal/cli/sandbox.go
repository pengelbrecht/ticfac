package cli

// `ticfac sandbox` — the per-repo sandbox definition and boot questions,
// ported from ticks' cmd/tk/cmd/sandbox.go at 7b6c0b2f^ (the chz cut that
// made ticks tracker-only deleted it). The image's run scripts call these verbs
// after their clone: the container shells out rather than teaching a shell
// script to parse TOML, and the verbs live here because the container's
// binaries are ticfac and a tracker-only tk.
//
// The engines are internal/sandbox (itself a port of ticks internal/sandbox);
// this file is only the command surface: flags, exit codes, and the stdout a
// boot script parses. Those outputs are contracts, so they are ported
// byte-for-byte where a script reads them — the two-line substrate answer, the
// single-line model, the image reference, the worker prompt — and the notes a
// human reads go to stderr exactly as tk printed them.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/sandbox"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

func newSandboxCommand(stdout, stderr io.Writer) *cobra.Command {
	var sandboxRoot string
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Inspect and apply this repo's [sandbox] declaration",
		Long: `Report and apply the ` + "`[sandbox]`" + ` table of .tick/runners.toml.

A repository declares its own sandbox in tracked config: the image it boots,
extra toolchain provisioned through the version manager the base image ships,
and idempotent setup commands that warm its caches. Declaring nothing — the
usual case — means the version-pinned base image and no setup.

The setup commands run arbitrary shell inside a sandbox that holds a run's
credentials, so they are read ONLY from the tracked, PR-reviewed config in the
checkout: never from a tick note, a model, a signal payload or an API
parameter. Adding capability to a sandbox is a pull request.`,
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.PersistentFlags().StringVar(&sandboxRoot, "root", "",
		"checkout to read .tick/runners.toml from (default: the repo containing the working directory)")
	cmd.AddCommand(
		newSandboxImageCommand(stdout, stderr, &sandboxRoot),
		newSandboxToolchainCommand(stdout, stderr, &sandboxRoot),
		newSandboxModelCommand(stdout, stderr, &sandboxRoot),
		newSandboxSubstrateCommand(stdout, stderr, &sandboxRoot),
		newSandboxSetupCommand(stdout, stderr, &sandboxRoot),
		newSandboxEnvironmentCommand(stdout, stderr, &sandboxRoot),
		newSandboxWorkerPromptCommand(stdout, stderr, &sandboxRoot),
	)
	return cmd
}

// sandboxCheckout resolves the checkout to read. `--root` wins so the caller
// that has just cloned somewhere (the sandbox entrypoint) does not have to cd.
func sandboxCheckout(root string) (string, error) {
	if root != "" {
		return root, nil
	}
	found, err := skillsRepoRoot()
	if err != nil {
		return "", newExitError(exitNoRepo, "failed to detect repo root: %v", err)
	}
	return found, nil
}

// ---------------------------------------------------------------------------
// ticfac sandbox image
// ---------------------------------------------------------------------------

func newSandboxImageCommand(stdout, stderr io.Writer, root *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Print the image reference this repo's sandbox boots",
		Long: `Print the image a sandbox for this checkout boots.

That is ` + "`sandbox.image`" + ` when the repository declares one, else the base image
pinned to the tk version the image build carries. Whatever boots a sandbox
takes the reference as a parameter, so this is the value it asks for.`,
		Args: cobra.NoArgs,
	}
	tkVersion := cmd.Flags().String("tk-version", "",
		"tk version the base image is pinned to (default: the version this image tree's Dockerfile pins)")
	declaredOnly := cmd.Flags().Bool("declared-only", false,
		"print only an image the repository itself declares, and nothing when it declares none")
	asJSON := cmd.Flags().Bool("json", false,
		"print one versioned document (ticfac.sandbox-image.v1): the image reference, whether the repository declared it, and the base it falls back to")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(reportCommand("sandbox image", sandboxImageCommand(c, *root, *tkVersion, *declaredOnly, *asJSON, stdout, stderr), stderr))
	}
	return cmd
}

func sandboxImageCommand(c *cobra.Command, root, tkVersionRaw string, declaredOnly, asJSON bool, stdout, stderr io.Writer) error {
	checkout, err := sandboxCheckout(root)
	if err != nil {
		return err
	}
	version := tkVersionRaw
	if version == "" {
		// The base image is tagged with the tk version the image embeds, so
		// the default is the Dockerfile's own pin — read, never typed a second
		// time, so a bump in image/ is a bump here too.
		version, err = sandboximage.PinnedTkVersion()
		if err != nil {
			return newExitError(exitGeneric, "the base image's tk version: %v", err)
		}
	}
	ref, declared, err := sandbox.Image(checkout, sandbox.BaseImage(version))
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}
	if declaredOnly && !declared {
		// Silence is the answer: this repository asks for no image of its own,
		// which is the 99% path. A caller comparing what it booted against
		// what the repository wants has nothing to compare.
		return nil
	}
	if asJSON {
		return emitAgentJSON(stdout, struct {
			agentDoc
			Image     string `json:"image"`
			Declared  bool   `json:"declared"`
			BaseImage string `json:"base_image"`
		}{
			agentDoc:  agentDoc{Schema: agentSchemaID("sandbox-image"), State: agentStateDone},
			Image:     ref,
			Declared:  declared,
			BaseImage: sandbox.BaseImage(version),
		})
	}
	fmt.Fprintln(stdout, ref)
	if declared {
		// Never silent: a repository asking for its own image is a fact the
		// operator reading a boot log should see, not a value substituted
		// behind their back.
		fmt.Fprintf(stderr, "note: %s declares this image; whatever boots the sandbox must pass it\n", checkout)
	}
	return nil
}

// ---------------------------------------------------------------------------
// ticfac sandbox toolchain
// ---------------------------------------------------------------------------

func newSandboxToolchainCommand(stdout, stderr io.Writer, root *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "toolchain",
		Short: "Print the extra tool@version pins this repo declares",
		Long: `Print the ` + "`sandbox.toolchain`" + ` pins, one per line, in file order.

These are the tools the base image does not carry, provisioned through its
version manager into the project's persistent cache on first run and warm
after. Ecosystem pins the image already reads on its own (go.mod,
package.json's packageManager, .node-version, .tool-versions) do not belong
here and are not printed.`,
		Args: cobra.NoArgs,
	}
	asJSON := cmd.Flags().Bool("json", false,
		"print one versioned document (ticfac.sandbox-toolchain.v1): the declared tool@version pins, in file order")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(reportCommand("sandbox toolchain", sandboxToolchainCommand(c, *root, *asJSON, stdout, stderr), stderr))
	}
	return cmd
}

func sandboxToolchainCommand(c *cobra.Command, root string, asJSON bool, stdout, stderr io.Writer) error {
	checkout, err := sandboxCheckout(root)
	if err != nil {
		return err
	}
	specs, err := sandbox.Toolchain(checkout)
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}
	if asJSON {
		return emitAgentJSON(stdout, struct {
			agentDoc
			Toolchain []string `json:"toolchain"`
		}{
			agentDoc:  agentDoc{Schema: agentSchemaID("sandbox-toolchain"), State: agentStateDone},
			Toolchain: specs,
		})
	}
	for _, spec := range specs {
		fmt.Fprintln(stdout, spec)
	}
	return nil
}

// ---------------------------------------------------------------------------
// ticfac sandbox model
// ---------------------------------------------------------------------------

func newSandboxModelCommand(stdout, stderr io.Writer, root *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Print the model this repo routes the orchestrator to",
		Long: `Print the model a cloud container booting on this checkout runs on.

With no ` + "`--role`" + `, that is the orchestrator's cell: ` + "`orchestrator.model`" + ` when the
repository sets one, else the role/tier table resolved for role ` + "`orchestrator`" + `
at the frontier tier — which falls back to ` + "`roles.implement`" + ` like any other
role the config does not name.

With ` + "`--role`" + `, it is that role's cell of the role/tier table, at ` + "`--tier`" + ` when
one is given. A per-tick worker container asks for ` + "`--role implement`" + `: the
orchestrator's frontier cell plans waves and reviews epics, and routing every
worker at it is a silent multiple on a wave's bill. ` + "`orchestrator.model`" + ` is not
consulted for a named role — that entry is the orchestrator's.

Nothing is printed when the config routes no model. That is not a default: a
harness handed no model does not fail, it hangs, so whatever boots one refuses
to start rather than guessing a model on the repository's behalf.`,
		Args: cobra.NoArgs,
	}
	role := cmd.Flags().String("role", "", "role cell to resolve (default: the orchestrator's own cell)")
	tier := cmd.Flags().String("tier", "", "tier to resolve --role at (economy, balanced, strong, frontier)")
	asJSON := cmd.Flags().Bool("json", false,
		"print one versioned document (ticfac.sandbox-model.v1): the routed model and the cell it came from, empty model and all")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(reportCommand("sandbox model", sandboxModelCommand(c, *root, *role, *tier, *asJSON, stdout, stderr), stderr))
	}
	return cmd
}

func sandboxModelCommand(c *cobra.Command, root, role, tier string, asJSON bool, stdout, stderr io.Writer) error {
	checkout, err := sandboxCheckout(root)
	if err != nil {
		return err
	}
	if tier != "" && !knownTier(tier) {
		return newExitError(exitUsage, "unknown --tier %q (want one of %s)", tier, tierNames())
	}
	var routed sandbox.RoutedModel
	if role == "" {
		routed, err = sandbox.OrchestratorModel(checkout)
	} else {
		routed, err = sandbox.RoleModel(checkout, role, runconfig.Tier(tier))
	}
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}
	if routed.Model == "" {
		// Silence on stdout is the answer a caller parses; the reason goes to
		// stderr, so a boot log says WHY there is no model rather than only
		// that there is none.
		if role == "" {
			fmt.Fprintf(stderr,
				"note: %s routes no model for the orchestrator — set [orchestrator].model, or a model on the role it falls back to ([roles.implement])\n",
				runconfig.FileName)
		} else {
			fmt.Fprintf(stderr,
				"note: %s routes no model for role %s — set a model on [roles.%s] (or on [roles.implement], which it falls back to)\n",
				runconfig.FileName, role, role)
		}
	}
	if asJSON {
		// An empty model is an ANSWER — "this config routes none" — not a
		// failure, so the document carries it rather than omitting the field.
		return emitAgentJSON(stdout, struct {
			agentDoc
			Role   string `json:"role"`
			Model  string `json:"model"`
			Source string `json:"source"`
		}{
			agentDoc: agentDoc{Schema: agentSchemaID("sandbox-model"), State: agentStateDone},
			Role:     role,
			Model:    routed.Model,
			Source:   routed.Source,
		})
	}
	if routed.Model == "" {
		return nil
	}
	fmt.Fprintln(stdout, routed.Model)
	fmt.Fprintf(stderr, "note: routed by %s\n", routed.Source)
	return nil
}

func knownTier(name string) bool {
	for _, t := range runconfig.TierNames {
		if string(t) == name {
			return true
		}
	}
	return false
}

func tierNames() string {
	names := make([]string, 0, len(runconfig.TierNames))
	for _, t := range runconfig.TierNames {
		names = append(names, string(t))
	}
	return strings.Join(names, ", ")
}

// ---------------------------------------------------------------------------
// ticfac sandbox substrate
// ---------------------------------------------------------------------------

func newSandboxSubstrateCommand(stdout, stderr io.Writer, root *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "substrate",
		Short: "Print the dispatch substrate a run on this checkout resolves",
		Long: `Print the substrate this run dispatches workers through, and the note to record.

Two lines on stdout, in this order: the resolved substrate (` + "`herdr`" + `,
` + "`harness`" + ` or ` + "`cloud`" + `), and the ` + "`runner-state:`" + ` line to record on the epic with
` + "`tk note`" + `. Why it resolved that way — and the wave width that comes with it —
goes to stderr, where a boot log reads it.

Resolution is the decision procedure of runners-config.md, with one addition:
$` + runconfig.SubstrateEnvVar + ` overrides ` + "`[orchestration].substrate`" + ` for this run.
That is how whatever BOOTS a run states the substrate that is effective where it
actually executes — a cloud sandbox has no herdr server, while the same
repository's local runs orchestrate through herdr. The checkout is read, never
rewritten: the file keeps saying what the repository means everywhere else.

An override to ` + "`harness`" + ` or ` + "`cloud`" + ` is terminal and probes nothing.
An override to ` + "`herdr`" + ` probes read-only exactly as a pinned ` + "`herdr`" + ` does,
and degrades explicitly — announced and noted — when herdr is unavailable. A
value that is not a substrate is a stop, never a silent fall back to the file.`,
		Args: cobra.NoArgs,
	}
	asJSON := cmd.Flags().Bool("json", false,
		"print one versioned document (ticfac.sandbox-substrate.v1): the resolved substrate, the runner-state note to record, and the wave width that travels with it")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(reportCommand("sandbox substrate", sandboxSubstrateCommand(c, *root, *asJSON, stdout, stderr), stderr))
	}
	return cmd
}

func sandboxSubstrateCommand(c *cobra.Command, root string, asJSON bool, stdout, stderr io.Writer) error {
	checkout, err := sandboxCheckout(root)
	if err != nil {
		return err
	}
	resolved, err := sandbox.OrchestratorSubstrate(c.Context(), sandbox.SubstrateOptions{
		Root:     checkout,
		Override: os.Getenv(runconfig.SubstrateEnvVar),
	})
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}

	if asJSON {
		return emitAgentJSON(stdout, struct {
			agentDoc
			Substrate   string `json:"substrate"`
			Note        string `json:"note"`
			MaxParallel int    `json:"max_parallel"`
		}{
			agentDoc:    agentDoc{Schema: agentSchemaID("sandbox-substrate"), State: agentStateDone},
			Substrate:   string(resolved.Substrate),
			Note:        resolved.NoteLine(),
			MaxParallel: resolved.MaxParallel,
		})
	}

	// stdout is the machine contract: the substrate a script acts on, then the
	// note a run records. Both, because a caller that had to derive the note
	// itself would derive a second spelling of it.
	fmt.Fprintln(stdout, string(resolved.Substrate))
	fmt.Fprintln(stdout, resolved.NoteLine())

	// stderr is the reason, and there is always one. A degradation or an
	// override is stated loudly because the protocol requires it; the ordinary
	// paths state themselves quietly, because a boot log is the only record an
	// ephemeral sandbox leaves and "harness" with no explanation beside it is
	// indistinguishable from a run that never looked.
	fmt.Fprintf(stderr, "note: %s\n", resolved.Resolution())
	if resolved.MaxParallel > 0 {
		// The other `[orchestration]` key a cloud boot inherits from a local
		// pin. It is honoured, not overridden — but under the harness substrate
		// it means concurrent subagents inside ONE sandbox rather than
		// independent panes, which is worth saying out loud.
		fmt.Fprintf(stderr, "note: wave width %d from %s [orchestration].max_parallel",
			resolved.MaxParallel, runconfig.FileName)
		switch resolved.Substrate {
		case runconfig.SubstrateHarness:
			fmt.Fprint(stderr, " — under the harness substrate that is concurrent subagents in one sandbox")
		case runconfig.SubstrateCloud:
			fmt.Fprint(stderr, " — under the cloud substrate that is concurrent worker containers, and a deployment's own instance ceiling can lower it further")
		}
		fmt.Fprintln(stderr)
		// Said once, here, because this line used to be the whole of the
		// width: run_62c289d1 printed it and then dispatched seven. It is now
		// enforced where a dispatch is admitted, so a reader of the boot log
		// knows the number is binding rather than advisory.
		fmt.Fprintf(stderr,
			"note: the width is enforced on the dispatch path — claiming a %d+1th tick in a wave (tk update --status in_progress, ticfac herd spawn) is refused with exit %d until a slot frees\n",
			resolved.MaxParallel, tkExitDispatchWidth)
	}
	return nil
}

// tkExitDispatchWidth is tk's refusal code when a claim would exceed the
// configured dispatch width (internal/tk.ExitDispatchWidth, tk's exit 8): the
// number a boot log can name without a shell branching on it.
const tkExitDispatchWidth = 8

// ---------------------------------------------------------------------------
// ticfac sandbox setup
// ---------------------------------------------------------------------------

func newSandboxSetupCommand(stdout, stderr io.Writer, root *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Run this repo's declared setup commands, once per checkout",
		Long: `Run the ` + "`sandbox.setup`" + ` commands of .tick/runners.toml, in order, once.

"Once" is per checkout: the record of what ran lives in the checkout's git
directory, so a fresh clone or a new worker worktree warms again — its working
tree is as cold as its caches are warm — and a repeat call in the same checkout
does nothing. Setup commands must be idempotent regardless; the record buys
time, never correctness.

A failing command is a stop that names it, and leaves no record: a
half-provisioned sandbox that reports itself warm is worse than a cold one.`,
		Args: cobra.NoArgs,
	}
	force := cmd.Flags().Bool("force", false, "run the setup commands even when this checkout is already warm")
	stamp := cmd.Flags().String("stamp", "", "path of the warm record (default: <git-dir>/ticks/sandbox-setup)")
	asJSON := cmd.Flags().Bool("json", false,
		"print one versioned document (ticfac.sandbox-setup.v1): what was declared, what ran, and whether the checkout was already warm. The commands' own output goes to stderr, so stdout stays the document's alone")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(reportCommand("sandbox setup", sandboxSetupCommand(c, *root, *force, *stamp, *asJSON, stdout, stderr), stderr))
	}
	return cmd
}

func sandboxSetupCommand(c *cobra.Command, root string, force bool, stamp string, asJSON bool, stdout, stderr io.Writer) error {
	checkout, err := sandboxCheckout(root)
	if err != nil {
		return err
	}
	// With --json the setup commands' own output goes to stderr: stdout is
	// the document's alone, and a log a human reads still exists.
	out := stdout
	if asJSON {
		out = stderr
	}
	res, err := sandbox.Setup(c.Context(), sandbox.SetupOptions{
		Root:  checkout,
		Out:   out,
		Force: force,
		Stamp: stamp,
	})
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}
	if asJSON {
		return emitAgentJSON(stdout, struct {
			agentDoc
			Declared    int      `json:"declared"`
			Ran         []string `json:"ran"`
			Skipped     bool     `json:"skipped"`
			Stamp       string   `json:"stamp,omitempty"`
			Fingerprint string   `json:"fingerprint,omitempty"`
		}{
			agentDoc:    agentDoc{Schema: agentSchemaID("sandbox-setup"), State: agentStateDone},
			Declared:    res.Declared,
			Ran:         res.Ran,
			Skipped:     res.Skipped,
			Stamp:       res.Stamp,
			Fingerprint: res.Fingerprint,
		})
	}
	if res.Declared == 0 {
		fmt.Fprintf(stdout, "sandbox: no [sandbox] setup in %s — nothing to warm\n", runconfig.FileName)
	}
	return nil
}

// ---------------------------------------------------------------------------
// ticfac sandbox environment
// ---------------------------------------------------------------------------

func newSandboxEnvironmentCommand(stdout, stderr io.Writer, root *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "environment",
		Short: "Run this repo's declared environment checks",
		Long: `Run the run-start checks from [environment.commands] in .tick/runners.toml.

The checks are verification only: they test the checkout's environment before
the first wave and never ask for human action. A repository with no checks is a
successful, explicit no-op; an unreadable config or a failing check is a stop.`,
		Args: cobra.NoArgs,
	}
	asJSON := cmd.Flags().Bool("json", false,
		"print one versioned document (ticfac.sandbox-environment.v1): the checks declared, the ones that passed and the ones that failed. The checks' own output goes to stderr, so stdout stays the document's alone")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(reportCommand("sandbox environment", sandboxEnvironmentCommand(c, *root, *asJSON, stdout, stderr), stderr))
	}
	return cmd
}

func sandboxEnvironmentCommand(c *cobra.Command, root string, asJSON bool, stdout, stderr io.Writer) error {
	checkout, err := sandboxCheckout(root)
	if err != nil {
		return err
	}
	// With --json the checks' own output goes to stderr: stdout is the
	// document's alone, and a log a human reads still exists.
	out := stdout
	if asJSON {
		out = stderr
	}
	res, err := sandbox.Environment(c.Context(), sandbox.EnvironmentOptions{
		Root:    checkout,
		Out:     out,
		Timeout: sandboxEnvironmentTimeout(),
	})
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}
	if asJSON {
		return emitAgentJSON(stdout, struct {
			agentDoc
			Declared int      `json:"declared"`
			Passed   int      `json:"passed"`
			Failed   []string `json:"failed"`
		}{
			agentDoc: agentDoc{Schema: agentSchemaID("sandbox-environment"), State: agentStateDone},
			Declared: res.Declared,
			Passed:   res.Passed,
			Failed:   res.Failed,
		})
	}
	return nil
}

func sandboxEnvironmentTimeout() time.Duration {
	const defaultTimeout = 120 * time.Second
	raw := os.Getenv("TICKS_PREFLIGHT_TIMEOUT")
	if raw == "" {
		return defaultTimeout
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return defaultTimeout
	}
	return time.Duration(seconds) * time.Second
}

// ---------------------------------------------------------------------------
// ticfac sandbox worker-prompt
//
// The job one per-tick worker container is given, rendered from the tracker in
// the checkout it has just cloned.
//
// It exists so the container's entrypoint (image/worker.sh) never learns the
// tracker format — the same delegation as `ticfac sandbox model` and
// `ticfac sandbox setup`.
//
// The tick is read from the checkout and from nowhere else, so what a worker is
// asked to do comes from the tracker state at the submitted SHA. Reading the
// record file directly is the reconciler's own sanctioned evidence read (a
// tracker record is "evidence about a TREE", internal/reconcile/tracker.go):
// the manifest commands answer questions about a tracker's live state, and
// none of them asks "what does the record at this commit say".
// ---------------------------------------------------------------------------

func newSandboxWorkerPromptCommand(stdout, stderr io.Writer, root *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker-prompt",
		Short: "Print the prompt a per-tick worker container runs on",
		Long: `Print the job one per-tick worker container is given for a tick.

Rendered from the tick in this checkout with the shared worker prompt template,
plus the facts that are true only in a container: the checkout is a clone
rather than a worktree, nothing but the pushed branch survives, and the
entrypoint — not the agent — commits the report and pushes.

The tick is read from the checkout and from nowhere else, so what a worker is
asked to do comes from the tracker state at the submitted SHA.`,
		Args: cobra.NoArgs,
	}
	tick := cmd.Flags().String("tick", "", "the tick the worker implements (required)")
	branch := cmd.Flags().String("branch", "", "the branch the worker works on (default: tick/<epic>/<tick>)")
	base := cmd.Flags().String("base", "", "the base commit the branch was cut from, named in the prompt so the worker can verify it")
	asJSON := cmd.Flags().Bool("json", false,
		"print one versioned document (ticfac.sandbox-worker-prompt.v1): the tick, the branch and the rendered prompt")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(reportCommand("sandbox worker-prompt", sandboxWorkerPromptCommand(c, *root, *tick, *branch, *base, *asJSON, stdout, stderr), stderr))
	}
	return cmd
}

// sandboxTickRecord is the slice of the tracker record the prompt renders
// from. The shape is the pinned tracker layout's; unknown fields are ignored
// so a record the layout grew does not stop a boot.
type sandboxTickRecord struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	Description        string `json:"description"`
	Parent             string `json:"parent"`
	Type               string `json:"type"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
}

func sandboxWorkerPromptCommand(c *cobra.Command, root, tickID, branch, base string, asJSON bool, stdout, stderr io.Writer) error {
	if tickID == "" {
		return newExitError(exitUsage, "--tick is required: a worker container implements exactly one tick")
	}
	checkout, err := sandboxCheckout(root)
	if err != nil {
		return err
	}

	t, err := readSandboxTick(checkout, tickID)
	if err != nil {
		// The same boundary every other lookup draws: only an ABSENT tick is a
		// lookup miss. A tick file that exists and cannot be parsed is a real
		// failure — booting a worker on top of damaged state is worse than
		// not booting.
		var missing *sandboxTickAbsent
		if errors.As(err, &missing) {
			return newExitError(exitNotFound, "%v", err)
		}
		return newExitError(exitGeneric, "%v", err)
	}

	var epic sandboxTickRecord
	if t.Parent != "" {
		if parent, err := readSandboxTick(checkout, t.Parent); err == nil {
			epic = parent
		} else {
			epic.ID = t.Parent
		}
	}

	if branch == "" {
		branch = sandboximage.WorkerBranch(epic.ID, t.ID)
	}

	prompt := sandbox.BuildPrompt(sandbox.PromptInput{
		TickID:      t.ID,
		Title:       t.Title,
		Description: t.Description,
		Acceptance:  t.AcceptanceCriteria,
		EpicID:      epic.ID,
		EpicTitle:   epic.Title,
		Branch:      branch,
		Base:        base,
	})

	if asJSON {
		return emitAgentJSON(stdout, struct {
			agentDoc
			TickID string `json:"tick_id"`
			Branch string `json:"branch"`
			Prompt string `json:"prompt"`
		}{
			agentDoc: agentDoc{Schema: agentSchemaID("sandbox-worker-prompt"), State: agentStateDone},
			TickID:   t.ID,
			Branch:   branch,
			Prompt:   prompt + sandboximage.WorkerPromptAddendum(t.ID, branch),
		})
	}
	fmt.Fprint(stdout, prompt)
	fmt.Fprint(stdout, sandboximage.WorkerPromptAddendum(t.ID, branch))
	return nil
}

// sandboxTickAbsent marks the one read error that is a lookup miss rather
// than a failure. Its message is tk's, word for word (sandbox_parity_test.go).
type sandboxTickAbsent struct {
	id  string
	err error
}

func (e *sandboxTickAbsent) Error() string {
	return fmt.Sprintf("failed to read tick %s: read tick %s: %v", e.id, e.id, e.err)
}

func (e *sandboxTickAbsent) Unwrap() error { return e.err }

// readSandboxTick reads one tracker record out of the checkout at the state it
// is in — the submitted SHA in a container, the worktree's base locally.
func readSandboxTick(checkout, id string) (sandboxTickRecord, error) {
	path := filepath.Join(checkout, ".tick", "issues", id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return sandboxTickRecord{}, &sandboxTickAbsent{id: id, err: err}
		}
		return sandboxTickRecord{}, fmt.Errorf("failed to read tick %s: read tick %s: %w", id, id, err)
	}
	var t sandboxTickRecord
	if err := json.Unmarshal(data, &t); err != nil {
		return sandboxTickRecord{}, fmt.Errorf("failed to read tick %s: parse tick %s: %w", id, id, err)
	}
	return t, nil
}
