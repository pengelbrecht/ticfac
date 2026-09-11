package herdr

import (
	"fmt"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The worker prompt, herdr's variant.
//
// The contract is the local subprocess executor's — the role prompt opens,
// the report path is ABSOLUTE and executor-owned, the boundary is stated as a
// fact about the substrate — because it is the same job contract, delivered
// through a different agent. What differs is the delivery: a herdr worker
// runs in the workspace herdr opened on the worktree, so its working
// directory IS the worktree, and there is no supervisor environment to hang
// $TICFAC_RESULT_PATH on. The one claim the subprocess prompt makes that this
// one must not is therefore the environment-variable line: a herdr worker
// told "it is also in your environment as $TICFAC_RESULT_PATH" would be told
// a falsehood, and a worker that believed it over the absolute path would
// write its report nowhere.

func renderWorkerPrompt(record *attemptRecord, spec *subprocess.JobSpec) string {
	var b strings.Builder

	if role := strings.TrimSpace(record.RolePrompt); role != "" {
		fmt.Fprintf(&b, "%s\n\n", role)
		fmt.Fprintf(&b, "You are running in a herdr workspace on an isolated git worktree on branch %s: nobody will\n", record.Branch)
		fmt.Fprintf(&b, "answer a question, and the rest of this prompt is how this job is run.\n\n")
	} else {
		fmt.Fprintf(&b, "You are implementing one unit of work from the ticks tracker, in an isolated git\n")
		fmt.Fprintf(&b, "worktree on branch %s, running in a herdr workspace. You are running unattended:\n", record.Branch)
		fmt.Fprintf(&b, "nobody will answer a question.\n\n")
	}

	fmt.Fprintf(&b, "## The job\n\n")
	fmt.Fprintf(&b, "- role: %s\n", spec.Role)
	if record.Model != "" {
		fmt.Fprintf(&b, "- model: %s\n", record.Model)
	}
	fmt.Fprintf(&b, "- job: %s (attempt %d)\n", spec.JobID, record.Attempt)
	for _, in := range spec.Inputs {
		fmt.Fprintf(&b, "- %s: %s\n", in.Kind, in.ID)
	}
	fmt.Fprintf(&b, "- worktree: %s (your working directory)\n", record.Worktree)
	fmt.Fprintf(&b, "- branch: %s (from %s)\n", record.Branch, short(record.BaseSHA))
	fmt.Fprintf(&b, "- wall clock: %d seconds\n\n", record.WallSeconds)

	if record.TickID != "" && record.TickID != "job" {
		fmt.Fprintf(&b, "The unit of work is recorded at .tick/issues/%s.json in this worktree. Read it there,\n", record.TickID)
		fmt.Fprintf(&b, "along with .tick/config.md and .tick/learnings.md if they exist, and the repository's\n")
		fmt.Fprintf(&b, "own instruction file. Do not run `tk`.\n\n")
	}

	fmt.Fprintf(&b, "## Your report — the ONLY channel\n\n")
	fmt.Fprintf(&b, "Write your report to this EXACT ABSOLUTE PATH:\n\n    %s\n\n", record.ResultPath)
	fmt.Fprintf(&b, "Write it there whatever your working directory is when you finish — the path is\n")
	fmt.Fprintf(&b, "absolute precisely so that changing directory cannot lose your report. Terminal\n")
	fmt.Fprintf(&b, "output is not read.\n\n")
	fmt.Fprintf(&b, "The report must end with a final line that is exactly one of:\n\n")
	fmt.Fprintf(&b, "    STATUS: %s\n    STATUS: %s — <what to double-check>\n    STATUS: %s — <what you need>\n    STATUS: %s — <why>\n\n",
		subprocess.StatusDone, subprocess.StatusDoneWithConcerns, subprocess.StatusNeedsContext, subprocess.StatusBlocked)
	fmt.Fprintf(&b, "A report with no recognisable status line reads as a missing report, whatever else\n")
	fmt.Fprintf(&b, "it says. Commit your source and tests on %s before you write it. Do NOT commit the\n", record.Branch)
	fmt.Fprintf(&b, "report itself — it lives under %s, which is the run's artifact space, not repository\n", spec.ArtifactPrefix)
	fmt.Fprintf(&b, "content.\n\n")

	fmt.Fprintf(&b, "## Boundaries\n\n")
	fmt.Fprintf(&b, "- Do not run `tk`, and do not write under .tick/ or .ticfac/. Those are the\n")
	fmt.Fprintf(&b, "  tracker's and the run's authorities, not yours. Every attempt is diffed against\n")
	fmt.Fprintf(&b, "  %s and reported, so a write there is found whether or not you mention it.\n", short(record.BaseSHA))
	fmt.Fprintf(&b, "- Work only inside this worktree. Do not touch sibling workspaces, worktrees or\n")
	fmt.Fprintf(&b, "  other branches.\n")
	fmt.Fprintf(&b, "- Commit source and tests only, never build output or caches.\n\n")

	if spec.OutputSchema != "" {
		fmt.Fprintf(&b, "The structured result this job was asked for is %s.\n", spec.OutputSchema)
	}
	return b.String()
}
