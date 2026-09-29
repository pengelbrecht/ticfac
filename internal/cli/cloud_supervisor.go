package cli

// `ticfac cloud supervisor`, ported from ticks' cmd/tk/cmd/cloud_supervisor.go
// with the cobra plumbing replaced by a flag set and the body otherwise
// verbatim.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pengelbrecht/ticfac/internal/factory"
	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
)

// cloudflareHTTPClient makes the Workflows read. A package variable for the
// same reason `cloudHTTPClient` is one: a command test exercises the protocol
// through a transport, never a loopback listener.
var cloudflareHTTPClient *http.Client

// newCloudSupervisorCommand builds `cloud supervisor`'s cobra command.
func newCloudSupervisorCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "supervisor <run-id|epic-id>",
		Short: "whether the Workflow is alive",
	}
	fs := newFlagSet("cloud supervisor", nil)
	steps := fs.Int("steps", 0, "also print the last N steps of the trail (0 prints the current step and every failed one)")
	asJSON := fs.Bool("json", false, "print one versioned document (ticfac.supervisor.v1): the Workflow's status, its disagreement with the run record when there is one, and the step trail")
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(runCloudSupervisor(c.Context(), args, steps, asJSON, stdout, stderr))
	}
	return cmd
}

func runCloudSupervisor(ctx context.Context, args []string, steps *int, asJSON *bool, stdout, stderr io.Writer) int {
	err := cloudSupervisor(ctx, args, steps, asJSON, stdout, stderr)
	return reportCommand("cloud supervisor", err, stderr)
}

func cloudSupervisor(ctx context.Context, args []string, steps *int, asJSON *bool, stdout, stderr io.Writer) error {
	rest := args
	if len(rest) != 1 || rest[0] == "" {
		return newExitError(exitUsage, "exactly one run id is required")
	}

	if *steps < 0 {
		return newExitError(exitGeneric, "--steps takes a step count, got %d", *steps)
	}
	// A whole run id never touches the factory here, which is what lets this
	// command answer while the deployment is the thing under suspicion.
	runID, err := cloudRunArg(ctx, "supervisor", rest[0], false, stderr)
	if err != nil {
		return err
	}
	opts, err := cloudSupervisorOptions()
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}
	supervisor, err := factory.ReadSupervisor(ctx, runID, opts)
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}
	if *asJSON {
		return emitCloudSupervisorJSON(supervisor, cloudRecordedRunState(ctx, runID), *steps, stdout)
	}
	printCloudSupervisor(stdout, supervisor, cloudRecordedRunState(ctx, runID), time.Now(), *steps)
	return nil
}

// emitCloudSupervisorJSON prints `cloud supervisor --json`'s one document,
// ticfac.cloud-supervisor.v1: the Workflow's own status read outside the
// deployment, the run record's claim beside it (the disagreement is the
// line worth the whole command, and it is a field here), and the step trail.
func emitCloudSupervisorJSON(supervisor *factory.Supervisor, recorded string, steps int, stdout io.Writer) error {
	current := supervisor.CurrentStep()
	failed := supervisor.FailedSteps()
	doc := struct {
		agentDoc
		RunID         string                   `json:"run_id"`
		Status        string                   `json:"status"`
		Alive         bool                     `json:"alive"`
		Explanation   string                   `json:"explanation"`
		Error         string                   `json:"error,omitempty"`
		Start         string                   `json:"start,omitempty"`
		End           string                   `json:"end,omitempty"`
		RecordedState string                   `json:"recorded_run_state,omitempty"`
		Disagreement  string                   `json:"disagreement,omitempty"`
		StepCount     int                      `json:"step_count"`
		CurrentStep   *factory.SupervisorStep  `json:"current_step,omitempty"`
		FailedSteps   []factory.SupervisorStep `json:"failed_steps"`
	}{
		agentDoc:      agentDoc{Schema: agentSchemaID("cloud-supervisor"), State: agentStateDone},
		RunID:         supervisor.RunID,
		Status:        supervisor.Status,
		Alive:         supervisor.Alive(),
		Explanation:   supervisor.Explain(),
		Error:         supervisor.Error.String(),
		Start:         supervisor.Start,
		End:           supervisor.End,
		RecordedState: recorded,
		Disagreement:  cloudSupervisorDisagreement(supervisor, recorded),
		StepCount:     len(supervisor.Steps),
		FailedSteps:   failed,
	}
	if current != nil {
		doc.CurrentStep = current
	}
	return emitAgentJSON(stdout, doc)
}

// cloudSupervisorOptions reads the operator's own Cloudflare credentials.
//
// The account comes off the gateway URL rather than a second stored copy, so
// there is one configured value naming the account and nothing to drift.
func cloudSupervisorOptions() (factory.SupervisorOptions, error) {
	config, err := factory.LoadCredentials()
	if err != nil {
		return factory.SupervisorOptions{}, fmt.Errorf("cannot read factory configuration: %w", err)
	}
	return factory.SupervisorOptions{
		HTTPClient:         cloudflareHTTPClient,
		GatewayURL:         strings.TrimSpace(config.Get(credentials.KeyGatewayURL)),
		CloudflareAPIToken: strings.TrimSpace(config.Get(credentials.KeyCloudflareAPIToken)),
	}, nil
}

// cloudRecordedRunState is what the run's own record claims, or "" when the
// factory could not be asked.
//
// Best effort on purpose. This command's whole value is that it answers
// without the deployment; a factory that is down must cost the caller the
// comparison, never the supervisor's verdict.
func cloudRecordedRunState(ctx context.Context, runID string) string {
	client, err := newCloudClient()
	if err != nil {
		return ""
	}
	data, err := client.request(ctx, http.MethodGet, "/api/runs/"+runID, nil)
	if err != nil {
		return ""
	}
	var response cloudStatusResponse
	if err := decodeCloudJSON(data, &response); err != nil {
		return ""
	}
	return strings.TrimSpace(response.Run.State)
}

func printCloudSupervisor(out io.Writer, supervisor *factory.Supervisor, recorded string, now time.Time, steps int) {
	fmt.Fprintf(out, "Supervisor of %s\n", supervisor.RunID)
	fmt.Fprintf(out, "  workflow: %s (read from Cloudflare, outside the supervisor)\n", factory.RunWorkflowName)
	fmt.Fprintf(out, "  status: %s — %s\n", stateOrUnknown(supervisor.Status), supervisor.Explain())
	if detail := supervisor.Error.String(); detail != "" {
		fmt.Fprintf(out, "  error: %s\n", detail)
	}
	if supervisor.Start != "" {
		fmt.Fprintf(out, "  started: %s\n", supervisor.Start)
	}
	if supervisor.End != "" {
		fmt.Fprintf(out, "  ended: %s\n", supervisor.End)
	}

	// The Phase 2 symptom, stated as a contradiction rather than left for a
	// reader to notice across two commands: the run record says one thing and
	// the thing that writes it is gone.
	if note := cloudSupervisorDisagreement(supervisor, recorded); note != "" {
		fmt.Fprintln(out, note)
	}

	fmt.Fprintf(out, "  steps: %d recorded\n", len(supervisor.Steps))
	printed := map[string]bool{}
	if current := supervisor.CurrentStep(); current != nil {
		fmt.Fprintf(out, "  step %d/%d: %s\n", len(supervisor.Steps), len(supervisor.Steps), current.Name)
		printCloudSupervisorStep(out, *current, now)
		printed[current.Name] = true
	}
	for _, step := range supervisor.FailedSteps() {
		if printed[step.Name] {
			continue
		}
		fmt.Fprintf(out, "  failed step: %s\n", step.Name)
		printCloudSupervisorStep(out, step, now)
		printed[step.Name] = true
	}
	if steps > 0 {
		trail := supervisor.Steps
		if len(trail) > steps {
			trail = trail[len(trail)-steps:]
		}
		fmt.Fprintf(out, "  trail (last %d of %d):\n", len(trail), len(supervisor.Steps))
		for _, step := range trail {
			fmt.Fprintf(out, "    %-32s %s\n", step.Name, cloudStepVerdict(step))
		}
	}
	if hint := cloudStepCapHint(supervisor); hint != "" {
		fmt.Fprintln(out, hint)
	}
}

// cloudSupervisorDisagreement is the line worth the whole command.
//
// A run record that still says `running` while its Workflow instance is
// errored, terminated or complete is not a stale read: it is the durable
// statement of a supervisor that never got to write its last one. Nothing is
// advancing that run and nothing ever will.
func cloudSupervisorDisagreement(supervisor *factory.Supervisor, recorded string) string {
	if recorded == "" || supervisor.Alive() || isFinishedCloudRun(recorded) {
		return ""
	}
	return fmt.Sprintf(
		"  DISAGREEMENT: the run record says %q, but its supervisor is %s.\n"+
			"    The record is written BY the supervisor, so it is frozen at the last value one wrote —\n"+
			"    nothing is advancing this run. Free the project lease with 'ticfac cloud stop %s --now'.",
		recorded, stateOrUnknown(supervisor.Status), supervisor.RunID)
}

func printCloudSupervisorStep(out io.Writer, step factory.SupervisorStep, now time.Time) {
	fmt.Fprintf(out, "      %s\n", cloudStepVerdict(step))
	if step.Running() {
		if started, err := time.Parse(time.RFC3339, step.Start); err == nil {
			fmt.Fprintf(out, "      on this step for %s (since %s)\n",
				now.UTC().Sub(started).Round(time.Second), step.Start)
		}
	}
	if reason := step.Reason(); reason != "" {
		fmt.Fprintf(out, "      %s\n", reason)
	}
}

// cloudStepVerdict is one step in one clause: what happened, and how long it
// took to happen.
func cloudStepVerdict(step factory.SupervisorStep) string {
	verdict := "ran"
	switch {
	case step.Failed():
		verdict = "FAILED"
	case step.Running():
		verdict = "in flight"
	case step.Type == "sleep":
		verdict = "slept"
	}
	parts := []string{verdict}
	if attempts := len(step.Attempts); attempts > 1 {
		parts = append(parts, fmt.Sprintf("%d attempts", attempts))
	}
	if span := cloudStepSpan(step); span != "" {
		parts = append(parts, span)
	}
	return strings.Join(parts, ", ")
}

func cloudStepSpan(step factory.SupervisorStep) string {
	started, startErr := time.Parse(time.RFC3339, step.Start)
	ended, endErr := time.Parse(time.RFC3339, step.End)
	if startErr != nil || endErr != nil {
		return ""
	}
	return ended.Sub(started).Round(time.Second).String()
}

// stepExecutionCapMs is Cloudflare's per-step EXECUTION cap, in milliseconds —
// the number in cloudflare/src/workflow-limits.ts. It is matched on the
// message rather than inferred from a duration, because the message is what
// Cloudflare actually returns and a duration would guess.
const stepExecutionCapMessage = "600000ms"

// cloudStepCapHint recognises the ten-minute cap and says what it means, so the
// next operator does not spend 2xm's day rediscovering that a step timeout
// fails the whole instance.
func cloudStepCapHint(supervisor *factory.Supervisor) string {
	hit := strings.Contains(supervisor.Error.String(), stepExecutionCapMessage)
	for _, step := range supervisor.FailedSteps() {
		if strings.Contains(step.Reason(), stepExecutionCapMessage) {
			hit = true
		}
	}
	if !hit {
		return ""
	}
	return "  note: 600000ms is Cloudflare's per-step EXECUTION cap (ten minutes), and it fails the whole\n" +
		"    instance rather than one step. A step that blocks for longer has to be spread across\n" +
		"    bounded legs — see cloudflare/src/workflow-limits.ts and repo-wiki/debugging-a-live-cloud-run.md."
}
