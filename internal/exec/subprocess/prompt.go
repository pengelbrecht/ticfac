package subprocess

import (
	"fmt"
	"strings"
)

// The worker prompt.
//
// One thing in it matters more than the rest: the RESULT path is ABSOLUTE and
// the executor owns it. A worker that is told "write RESULT-<id>.md in the
// repository root" writes it wherever its working directory happened to be by
// the time it finished, and an agent that cds into /tmp to try something is
// doing nothing wrong. The report is the completion contract, so the one place
// it may not be is "wherever".
//
// The boundary is stated too, and it is stated as a fact about the substrate
// rather than as a request: collect diffs the attempt against its base and
// REPORTS every tracker record it finds, whatever the prompt said (Appendix A
// #10 — compliance is a property of the model).

func renderPrompt(record *attemptRecord, spec *JobSpec) string {
	var b strings.Builder

	// The ROLE comes first, and it comes from the caller's profile: what this
	// job is is the profile's to say, and the mechanics below are the
	// executor's. With no role prompt the opening is the implementation
	// worker's, which is what every job was before profiles existed.
	if role := strings.TrimSpace(record.RolePrompt); role != "" {
		fmt.Fprintf(&b, "%s\n\n", role)
		fmt.Fprintf(&b, "You are running headless in an isolated git worktree on branch %s: nobody will\n", record.Branch)
		fmt.Fprintf(&b, "answer a question, and the rest of this prompt is how this job is run.\n\n")
	} else {
		fmt.Fprintf(&b, "You are implementing one unit of work from the ticks tracker, in an isolated git\n")
		fmt.Fprintf(&b, "worktree on branch %s. You are running headless: nobody will answer a question.\n\n", record.Branch)
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
	fmt.Fprintf(&b, "- worktree: %s\n", record.Worktree)
	fmt.Fprintf(&b, "- branch: %s (from %s)\n", record.Branch, short(record.BaseSHA))
	fmt.Fprintf(&b, "- wall clock: %d seconds; you are stopped at it\n\n", record.WallSeconds)

	// Where the work is written down. The JobSpec names its inputs by id and
	// carries no task text — the records are closed — and a worker may not run
	// `tk`, so the one place left is the tracker record in this worktree.
	if record.TickID != "" && record.TickID != "job" {
		fmt.Fprintf(&b, "The unit of work is recorded at .tick/issues/%s.json in this worktree. Read it there,\n", record.TickID)
		fmt.Fprintf(&b, "along with .tick/config.md and .tick/learnings.md if they exist, and the repository's\n")
		fmt.Fprintf(&b, "own instruction file. Do not run `tk`.\n\n")
	}

	fmt.Fprintf(&b, "## Your report — the ONLY channel\n\n")
	fmt.Fprintf(&b, "Write your report to this EXACT ABSOLUTE PATH:\n\n    %s\n\n", record.ResultPath)
	fmt.Fprintf(&b, "It is also in your environment as $TICFAC_RESULT_PATH. Write it there whatever your\n")
	fmt.Fprintf(&b, "working directory is when you finish — the path is absolute precisely so that\n")
	fmt.Fprintf(&b, "changing directory cannot lose your report. Terminal output is not read.\n\n")
	fmt.Fprintf(&b, "The report must end with a final line that is exactly one of:\n\n")
	fmt.Fprintf(&b, "    STATUS: %s\n    STATUS: %s — <what to double-check>\n    STATUS: %s — <what you need>\n    STATUS: %s — <why>\n\n",
		StatusDone, StatusDoneWithConcerns, StatusNeedsContext, StatusBlocked)
	fmt.Fprintf(&b, "A report with no recognisable status line reads as a missing report, whatever else\n")
	fmt.Fprintf(&b, "it says. Commit your source and tests on %s before you write it.\n\n", record.Branch)

	fmt.Fprintf(&b, "## Boundaries\n\n")
	fmt.Fprintf(&b, "- Do not run `tk`, and do not write under %s. Those are the tracker's and the\n",
		strings.Join(protectedPrefixes, " or "))
	fmt.Fprintf(&b, "  run's authorities, not yours. Every attempt is diffed against %s and reported,\n", short(record.BaseSHA))
	fmt.Fprintf(&b, "  so a write there is found whether or not you mention it.\n")
	fmt.Fprintf(&b, "- Work only inside this worktree. Do not touch sibling worktrees or other branches.\n")
	fmt.Fprintf(&b, "- Commit source and tests only, never build output or caches.\n\n")

	if spec.OutputSchema != "" {
		fmt.Fprintf(&b, "The structured result this job was asked for is %s.\n", spec.OutputSchema)
	}
	return b.String()
}

// runnerEnv is what the runner is told about its own job. TICFAC_RESULT_PATH
// is the one that matters: a runner that cannot parse prose can still read one
// environment variable.
func runnerEnv(record *attemptRecord, spec *JobSpec) []string {
	return []string{
		"TICFAC_RESULT_PATH=" + record.ResultPath,
		"TICFAC_WORKTREE=" + record.Worktree,
		"TICFAC_BRANCH=" + record.Branch,
		"TICFAC_BASE_SHA=" + record.BaseSHA,
		"TICFAC_JOB_ID=" + spec.JobID,
		"TICFAC_ATTEMPT=" + fmt.Sprint(record.Attempt),
		"TICFAC_TICK=" + record.TickID,
		"TICFAC_ROLE=" + spec.Role,
		"TICFAC_MODEL=" + record.Model,
		"TICFAC_PROMPT_FILE=" + record.State + "/" + filePrompt,
	}
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
