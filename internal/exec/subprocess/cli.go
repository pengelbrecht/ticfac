package subprocess

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

// The Unix-style command surface: one record in on stdin, one record out on
// stdout, and nothing else on stdout ever.
//
//	ticfac-exec-subprocess start   < job_spec.json    > job_handle.json
//	ticfac-exec-subprocess inspect < job_handle.json  > job_status.json
//	ticfac-exec-subprocess cancel  < job_handle.json  > cancel_ack.json
//	ticfac-exec-subprocess collect < job_handle.json  > job_result.json
//
// That is the whole protocol, and the argv is the contract's own
// (`operations[].argv` is "ticfac-exec-<name> <operation>"). `dispose`,
// `stop` and `supervise` are host operations that the four-verb seam
// deliberately does not carry; they are here because something has to run
// them, and they are named as local so nobody mistakes one for a fifth
// operation.

// Exit codes. A refusal has its own code because "this executor will not do
// that, and here is which rule" is a different answer from "something broke",
// and a caller should not have to parse prose to tell them apart.
const (
	ExitOK      = 0
	ExitError   = 1
	ExitUsage   = 2
	ExitRefused = 3
)

const cliUsage = `ticfac-exec-subprocess — the local subprocess executor

  start    < job_spec.json   > job_handle.json
  inspect  < job_handle.json > job_status.json
  cancel   < job_handle.json > cancel_ack.json
  collect  < job_handle.json > job_result.json

local (not part of the four-operation protocol):
  dispose  < job_handle.json    remove the attempt worktree and branch
  stop                          record the durable refusal to issue credentials
  supervise --state <dir>       run one attempt to settlement (started by start)

flags:
  --repo <dir>          the checkout attempts branch from (default: cwd)
  --state-dir <dir>     executor state root (default: $TICFAC_EXEC_STATE_DIR)
  --runner <name>       claude | codex | pi (default: $TICFAC_RUNNER, else claude)
  --attempt <n>         the attempt number this start is for (default: 1)
  --remote <name>       the remote in-progress work is pushed to (default: origin)
  --push-interval <s>   seconds between timed pushes (default: 60)
  --cursor <c>          inspect: resume the observation stream here
  --reason <text>       dispose/stop: the explicit recovery this is part of
  --keep-branch         dispose: remove the worktree and leave the branch
`

// Main runs one invocation and returns the process exit code. Everything is
// passed in rather than reached for, so the behaviour under test is the
// behaviour that ships.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, cliUsage)
		return ExitUsage
	}
	operation := args[0]

	fs := flag.NewFlagSet("ticfac-exec-subprocess "+operation, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		repo         = fs.String("repo", "", "the checkout attempts branch from")
		stateDir     = fs.String("state-dir", "", "executor state root")
		runner       = fs.String("runner", os.Getenv("TICFAC_RUNNER"), "claude | codex | pi")
		attempt      = fs.Int("attempt", 1, "the attempt number this start is for")
		remote       = fs.String("remote", "origin", "the remote in-progress work is pushed to")
		pushInterval = fs.Int("push-interval", int(DefaultPushInterval/time.Second), "seconds between timed pushes")
		cursor       = fs.String("cursor", "", "resume the observation stream here")
		reason       = fs.String("reason", "", "the explicit recovery this disposal or stop is part of")
		keepBranch   = fs.Bool("keep-branch", false, "dispose: leave the branch in place")
		state        = fs.String("state", "", "supervise: the attempt state directory")
	)
	if err := fs.Parse(args[1:]); err != nil {
		return ExitUsage
	}

	switch operation {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, cliUsage)
		return ExitOK
	case "supervise":
		if *state == "" {
			fmt.Fprintf(stderr, "supervise: --state is required\n")
			return ExitUsage
		}
		if err := Supervise(*state); err != nil {
			fmt.Fprintf(stderr, "supervise: %v\n", err)
			return ExitError
		}
		return ExitOK
	case "start", "inspect", "cancel", "collect", "dispose", "stop":
	default:
		fmt.Fprintf(stderr, "ticfac-exec-subprocess: unknown operation %q\n\n%s", operation, cliUsage)
		return ExitUsage
	}

	executor, err := New(Options{
		Repo:         *repo,
		StateDir:     *stateDir,
		Runner:       *runner,
		Attempt:      *attempt,
		Remote:       *remote,
		PushInterval: time.Duration(*pushInterval) * time.Second,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ticfac-exec-subprocess: %v\n", err)
		return ExitError
	}

	if operation == "stop" {
		if err := executor.RequestStop("operator", *reason); err != nil {
			fmt.Fprintf(stderr, "stop: %v\n", err)
			return ExitError
		}
		fmt.Fprintf(stderr, "stop recorded: no credential is issued and no attempt boots until it is removed by hand.\n")
		return ExitOK
	}

	input, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "%s: read stdin: %v\n", operation, err)
		return ExitError
	}
	if len(input) == 0 {
		fmt.Fprintf(stderr, "%s: this operation reads its input record on stdin\n", operation)
		return ExitUsage
	}

	var record any
	switch operation {
	case "start":
		spec, err := ParseJobSpec(input)
		if err != nil {
			return fail(stderr, operation, err)
		}
		record, err = executor.Start(spec)
		if err != nil {
			return fail(stderr, operation, err)
		}
	case "inspect":
		handle, err := ParseJobHandle(input)
		if err != nil {
			return fail(stderr, operation, err)
		}
		record, err = executor.Inspect(handle, *cursor)
		if err != nil {
			return fail(stderr, operation, err)
		}
	case "cancel":
		handle, err := ParseJobHandle(input)
		if err != nil {
			return fail(stderr, operation, err)
		}
		record, err = executor.Cancel(handle)
		if err != nil {
			return fail(stderr, operation, err)
		}
	case "collect":
		handle, err := ParseJobHandle(input)
		if err != nil {
			return fail(stderr, operation, err)
		}
		collected, err := executor.CollectDetail(handle)
		if err != nil {
			return fail(stderr, operation, err)
		}
		// The verdict's sentence goes to stderr: stdout carries the record and
		// nothing else, and a boundary attempt is REPORTED even when the
		// record has nowhere to put it.
		if collected.Message != "" {
			fmt.Fprintf(stderr, "%s: %s\n", collected.Verdict, collected.Message)
		}
		for _, path := range collected.BoundaryViolations {
			fmt.Fprintf(stderr, "boundary violation: the attempt wrote %s\n", path)
		}
		record = collected.Result
	case "dispose":
		handle, err := ParseJobHandle(input)
		if err != nil {
			return fail(stderr, operation, err)
		}
		if err := executor.Dispose(handle, DisposeOptions{Reason: *reason, KeepBranch: *keepBranch}); err != nil {
			return fail(stderr, operation, err)
		}
		return ExitOK
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(record); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", operation, err)
		return ExitError
	}
	return ExitOK
}

func fail(stderr io.Writer, operation string, err error) int {
	if refusal, ok := AsRefusal(err); ok {
		fmt.Fprintf(stderr, "%s refused (%s): %s\n", operation, refusal.Reason, refusal.Message)
		return ExitRefused
	}
	fmt.Fprintf(stderr, "%s: %v\n", operation, err)
	return ExitError
}
