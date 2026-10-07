package cloudflaresandbox

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	shorttest "github.com/pengelbrecht/ticfac/internal/shorttest"
)

// A claude-sub job on a HOSTED deployment, end to end through the REAL door
// (tick yhe): the door binds the fake WORKER_AGENTS seam — production's shape,
// under which every worker attempt used to be its agent's — the run's config
// resolves the subscription rung (claude on the versionless sonnet alias),
// and the REAL pool leases the run a subscription.
//
// What only the real thing can prove, and what this test asserts:
//
//   - the START is accepted by the real executor: the handle names the rung's
//     own pairing, claude/sonnet — the one profile.CloudBillingAllows admits —
//     never the agent's harness over an alias the factory's gateway cannot
//     serve, which is the pairing the client refuses (pinned on the client's
//     side by TestAHostedHandleOnTheSubscriptionRungIsRefused);
//   - the WORKER is the container's own claude CLI under the interception:
//     the container's environment carries the rung's harness and alias, the
//     placeholder OAuth token and the claude-sub marker, and the door asked
//     the binding to install the interception — while NO agent was started
//     for it, the negative the door's own answers cannot express;
//   - the READ follows the start: Inspect answers from the worker's
//     container, not from an agent that holds nothing, so the job is running
//     while it works and settles with its own exit code — the read that
//     answered `lost` forever for every rung job on a hosted deployment
//     before the routing;
//   - the LEASE ends with the job: live under the real pool while the worker
//     runs, released at its settlement, so the subscription's cap is free
//     for the next job rather than held to the lease's TTL.
//
// The one substitution is the container itself, through the SANDBOXES seam,
// and the agent namespace's, through the seam-shaped WORKER_AGENTS binding —
// the same substitutions the worker's own suite makes, by the same design. The
// pool, the lease, the cap and the release are the deployed Durable Object's.
//
// End-to-end by the shorttest discipline, like the real-door suite it joins.
func TestTheRealDoorRunsAClaudeSubJobOnAHostedDeployment(t *testing.T) {
	shorttest.EndToEnd(t)
	door := newRealDoorOpt(t, true)
	door.harness = "claude"
	door.model = "sonnet"
	const tickID = "yhe"

	ex := door.newExecutor(t.TempDir())
	spec := door.newSpec(tickID)

	// ------------------------------------------------------ the dispatch ---
	handle, err := ex.Start(spec)
	if err != nil {
		t.Fatalf("a claude/sonnet rung start through the hosted door was refused: %v", err)
	}
	payload, err := local(handle)
	if err != nil {
		t.Fatalf("decode the handle payload: %v", err)
	}
	if payload.Harness != "claude" || payload.Model != "sonnet" {
		t.Errorf("the handle names %s/%s, want the rung's own claude/sonnet: any other pairing "+
			"is one the cloud billing rule refuses", payload.Harness, payload.Model)
	}
	record, err := newStore(ex.stateDirFor(spec.JobID, ex.opts.Attempt)).readAttempt()
	if err != nil {
		t.Fatalf("read the attempt record: %v", err)
	}
	if record.Harness != "claude" || record.Model != "sonnet" {
		t.Errorf("the record names %s/%s, want the claude/sonnet rung it ran on", record.Harness, record.Model)
	}

	// The container's own claude worker: one live work process, booted on the
	// rung's pairing, carrying the placeholder and the marker — and never the
	// subscription's token.
	door.assertOneLiveWorkProcess(t, "after the rung dispatch")
	_, workEnv, err := door.observe()
	if err != nil {
		t.Fatalf("observe the booted container: %v", err)
	}
	if got := workEnv["CLAUDE_CODE_OAUTH_TOKEN"]; got != realDoorClaudeSubPlaceholder {
		t.Errorf("the container's OAuth token is %q, want the placeholder %q: the subscription's own token "+
			"never enters a container", got, realDoorClaudeSubPlaceholder)
	}
	if got := workEnv["TICKS_CLAUDE_SUB"]; got != "1" {
		t.Errorf("the container's claude-sub marker is %q, want \"1\": the entrypoint keys its "+
			"subscription route on it", got)
	}
	if got := workEnv["TICKS_HARNESS"]; got != "claude" {
		t.Errorf("the work process is bound to harness %q, want the rung's %q", got, "claude")
	}
	if got := workEnv["TICKS_MODEL"]; got != "sonnet" {
		t.Errorf("the work process was booted on model %q, want the rung's alias %q", got, "sonnet")
	}

	// The interception install was asked of the container binding, with the
	// lease's label and the job id the pool keyed the lease under — the
	// install the hosted path never made, which is half of what made every
	// rung job on a hosted deployment die.
	boots, err := door.observeClaudeSubBoots()
	if err != nil {
		t.Fatalf("observe the claude-sub boots: %v", err)
	}
	if len(boots) != 1 {
		t.Fatalf("the door asked for %d claude-sub interceptions, want exactly one for this job", len(boots))
	}
	if boots[0].Label != "MAX1" || boots[0].JobID != spec.JobID || boots[0].Name != payload.Sandbox {
		t.Errorf("the claude-sub interception is %#v, want label MAX1, job %s, container %s",
			boots[0], spec.JobID, payload.Sandbox)
	}

	// And no agent was started for it: the negative the door's own answers
	// cannot express, read off the seam the door consulted.
	started, err := door.observeAgentsStarted()
	if err != nil {
		t.Fatalf("observe the agents: %v", err)
	}
	if len(started) != 0 {
		t.Errorf("%d WorkerAgents were started for a job that runs in its own container: %v",
			len(started), started)
	}

	// ------------------------------------------------------------- the read ---
	// The state route follows the start's routing: the worker's CONTAINER
	// answers, not an agent that holds nothing — the read that would have
	// answered `lost` for as long as the job ran.
	status, err := ex.Inspect(handle, "")
	if err != nil {
		t.Fatalf("Inspect the rung job: %v", err)
	}
	if status.State != subprocess.StateRunning || status.Terminal {
		t.Fatalf("the rung job reads %s (terminal %v), want running and not terminal: the state route "+
			"did not follow the start to the container", status.State, status.Terminal)
	}
	if status.JobID != spec.JobID {
		t.Errorf("the status answers for %q, want %q", status.JobID, spec.JobID)
	}

	// The lease is live under the real pool, addressed by the attempt's job.
	if leases, err := door.observeClaudeSubLeases("MAX1"); err != nil {
		t.Fatalf("observe the pool: %v", err)
	} else if len(leases) != 1 || leases[0] != spec.JobID {
		t.Errorf("the subscription's live leases are %v, want exactly [%s]", leases, spec.JobID)
	}

	// -------------------------------------------------------- the settle ---
	// The worker finishes clean: read from its container, with its own exit
	// code — and the settlement reclaims the container and ends the lease,
	// so the subscription's cap is free for the next job.
	door.finish(t, payload.Sandbox, 0)
	settled, err := ex.Inspect(handle, "")
	if err != nil {
		t.Fatalf("Inspect the settled rung job: %v", err)
	}
	if settled.State != subprocess.StateSucceeded || !settled.Terminal {
		t.Fatalf("the settled rung job reads %s (terminal %v), want succeeded", settled.State, settled.Terminal)
	}
	if leases, err := door.observeClaudeSubLeases("MAX1"); err != nil {
		t.Fatalf("observe the pool after the settle: %v", err)
	} else if len(leases) != 0 {
		t.Errorf("the subscription's leases outlived the job: %v, want none", leases)
	}
}

// realDoorClaudeSubPlaceholder is the placeholder OAuth token the factory's
// claude-sub wiring hands the container (src/claude-sub.ts) — the value the
// interception swaps for the subscription's own token per request, so it is
// the one credential this job's environment may carry.
const realDoorClaudeSubPlaceholder = "ticfac-claude-sub-placeholder-not-a-token"

// observeClaudeSubBoots reads the claude-sub interceptions the door asked the
// container binding to install — with each lease's label and the job id the
// pool keyed it under.
func (d *realDoor) observeClaudeSubBoots() ([]realDoorClaudeSubBoot, error) {
	observation, err := d.rawObservation()
	if err != nil {
		return nil, err
	}
	return observation.ClaudeSubBoots, nil
}

// observeAgentsStarted reads every WorkerAgent the door started, by container
// name — the no-agent assertion for a job routed to its own container.
func (d *realDoor) observeAgentsStarted() (map[string]int, error) {
	observation, err := d.rawObservation()
	if err != nil {
		return nil, err
	}
	started := map[string]int{}
	for name, count := range observation.AgentsStarted {
		if count > 0 {
			started[name] = count
		}
	}
	return started, nil
}

// observeClaudeSubLeases reads the named subscription's live leases from the
// REAL pool, the way /api/claude-sub serves them: job ids, never token values.
func (d *realDoor) observeClaudeSubLeases(label string) ([]string, error) {
	response, err := http.Get(d.url + "/__door_harness/claude-sub")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var pool struct {
		Subscriptions []struct {
			Label        string   `json:"label"`
			ActiveLeases []string `json:"active_leases"`
		} `json:"subscriptions"`
	}
	if err := json.NewDecoder(response.Body).Decode(&pool); err != nil {
		return nil, err
	}
	for _, subscription := range pool.Subscriptions {
		if subscription.Label == label {
			return subscription.ActiveLeases, nil
		}
	}
	return nil, nil
}

// realDoorClaudeSubBoot is one interception install the door asked for, as the
// harness's observation route reports it.
type realDoorClaudeSubBoot struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	JobID string `json:"jobId"`
}

// realDoorObservation is the harness's whole observation answer: the fake
// bindings' own record of what the door asked them to boot (tick yhe grows it
// with the claude-sub installs and the agents started).
type realDoorObservation struct {
	Sandboxes []struct {
		Name      string `json:"name"`
		Processes []struct {
			ID       string            `json:"id"`
			Command  string            `json:"command"`
			State    string            `json:"state"`
			ExitCode *int              `json:"exit_code"`
			Env      map[string]string `json:"env"`
		} `json:"processes"`
	} `json:"sandboxes"`
	ClaudeSubBoots []realDoorClaudeSubBoot `json:"claude_sub_boots"`
	AgentsStarted  map[string]int          `json:"agents_started"`
}

func (d *realDoor) rawObservation() (realDoorObservation, error) {
	response, err := http.Get(d.url + "/__door_harness/sandboxes")
	if err != nil {
		return realDoorObservation{}, err
	}
	defer response.Body.Close()
	var observation realDoorObservation
	if err := json.NewDecoder(response.Body).Decode(&observation); err != nil {
		return realDoorObservation{}, err
	}
	return observation, nil
}
