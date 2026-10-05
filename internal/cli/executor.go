// The executor factory a production run uses: the routing tick to1 (epic av8)
// put behind `ticfac run-epic`. A dispatch goes through the executor its
// RESOLVED PROFILE names — the reconciler asks, this side answers, because a
// name the reconciler cannot spell (herdr, cloudflare-sandbox) is a name the
// reconciler must not spell: internal/reconcile carries no executor-specific
// code by design, and the seam tests in internal/exec/herdr and
// internal/exec/cloudflaresandbox enforce it.
//
// The honoured set is stated here too, as the KnownExecutors the reconciler's
// construction-time checks admit: the local subprocess executor this build
// always has, the herdr executor, and the cloudflare-sandbox executor that
// dispatches one attempt's worker container through the factory's per-tick
// sandbox door (registered by tick xev).
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/cloudflaresandbox"
	"github.com/pengelbrecht/ticfac/internal/exec/herdr"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
	"github.com/pengelbrecht/ticfac/internal/gatewaytrace"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// knownExecutors is what this build can honour, in the reconciler's own terms:
// each name a profile may name, with the runner names that executor can launch,
// whether it can tell each one which model to use, and the cadence at which a
// live job on it is addressed. The cadence is the executor's (tick u9l):
// seconds for the local substrates — nothing wipes an unaddressed job locally,
// and herdr has a push stream — and the reconciler's five-minute keepalive
// only where an executor states none of its own, which is the cloud's number.
func knownExecutors() []reconcile.KnownExecutor {
	return []reconcile.KnownExecutor{
		{
			Name:         subprocess.ExecutorName,
			Runners:      subprocess.KnownRunners(),
			AcceptsModel: subprocess.RunnerAcceptsModel,
			PollInterval: subprocess.PollInterval,
		},
		{
			Name:         herdr.ExecutorName,
			Runners:      runconfig.KnownKinds(),
			AcceptsModel: func(string) bool { return true },
			PollInterval: herdr.PollInterval,
		},
		{
			// The cloud executor (tick xev): the runner names a KIND the
			// sandbox image can run a harness for, and the poll cadence is the
			// executor's own five minutes, because on that substrate the poll
			// IS the keepalive. Any MODEL a profile names is accepted: the
			// dispatch door carries it (tick a08), the worker container boots
			// on it, and the executor refuses a handle naming any other — so
			// the recorded model is the one that ran, not the factory's own
			// default agreeing with it by luck. The profile's RUNNER and PROMPT
			// ride the same request (tick 9iz), for the same reason.
			Name:         cloudflaresandbox.ExecutorName,
			Runners:      runconfig.KnownKinds(),
			AcceptsModel: func(string) bool { return true },
			PollInterval: cloudflaresandbox.PollInterval,
			// The account's container ceiling less the orchestrator's own
			// container: the window admits no worker past it, so a start
			// never waits in the door for a slot (hn6's cloud run).
			MaxLiveJobs: cloudflaresandbox.WorkerSlots(),
		},
	}
}

// executorFactory is the NewExecutor a run is dispatched through: it routes on
// the dispatch's profile, falling back to the local executor when a dispatch
// carries no profile, exactly the way the runner flag always did. The
// subprocess half is reconcile.DefaultExecutor; the herdr half is built here,
// per dispatch, from the same runners.toml the gate is read from.
//
// gate is the runners.toml path: the herdr half compiles its agent argv from
// the [roles.*] table (full-auto template, model/effort flags, args) through
// the same validated reader everything else uses, and an EMPTY gate is the
// dispatch's own repository's .tick/runners.toml — the reconciler applies the
// same default to GateConfig, and a factory built with the flag's empty
// default must read the same file the reconciler does (tick 53k). runner is
// the operator's fallback for a dispatch whose profile resolved none.
func executorFactory(runner, gate string) func(reconcile.Dispatch) (reconcile.Executor, reconcile.Substrate, error) {
	local := reconcile.DefaultExecutor(runner, nil, pushInterval)
	return func(d reconcile.Dispatch) (reconcile.Executor, reconcile.Substrate, error) {
		if d.Profile == nil {
			return local(d)
		}
		switch d.Profile.Executor {
		case subprocess.ExecutorName:
			return local(d)
		case herdr.ExecutorName:
			return herdrExecutor(gate, d)
		case cloudflaresandbox.ExecutorName:
			return sandboxExecutor(d)
		default:
			// Unreachable in a run: usableProfile refused any profile naming an
			// executor outside knownExecutors() at construction. Refused again
			// here anyway, fail closed, because a factory that silently fell
			// back would dispatch through an executor the record does not name.
			return nil, reconcile.Substrate{}, fmt.Errorf(
				"the profile for %s names executor %q, which this build can honour none of %s",
				d.TickID, d.Profile.Executor, honouredNames())
		}
	}
}

// sweeperFactory builds the substrate half of the run's leftover sweep for the
// executor a profile routes to (reconcile/sweep.go). herdr makes resources
// outside git — workspaces and panes. The cloudflare-sandbox executor's
// workers leave boot markers (`<worker branch>-boot-stopped`, #176) on the
// remote under a name outside the run's namespace, which only it can spell;
// its sweeper needs no factory credential. Every other executor's leftovers
// are git's, which the run sweeps itself.
func sweeperFactory(gate string) func(reconcile.Dispatch) (reconcile.LeftoverSweeper, error) {
	return func(d reconcile.Dispatch) (reconcile.LeftoverSweeper, error) {
		if d.Profile != nil && d.Profile.Executor == cloudflaresandbox.ExecutorName {
			return cloudflaresandbox.NewLeftoverSweeper(d.Repo, d.Remote, d.EpicID, d.RunID), nil
		}
		if d.Profile == nil || d.Profile.Executor != herdr.ExecutorName {
			return nil, nil
		}
		executor, _, err := herdrExecutor(gate, d)
		if err != nil {
			return nil, err
		}
		sweeper, ok := executor.(reconcile.LeftoverSweeper)
		if !ok {
			return nil, fmt.Errorf("the herdr executor cannot sweep")
		}
		return sweeper, nil
	}
}

// honouredNames is the honoured set the way a refusal should spell it: the
// names a profile may name, comma-separated. Kept beside the set it renders
// so the two cannot drift.
func honouredNames() string {
	names := make([]string, 0, len(knownExecutors()))
	for _, known := range knownExecutors() {
		names = append(names, known.Name)
	}
	return strings.Join(names, ", ")
}

// sandboxExecutor builds the cloudflare-sandbox executor for one dispatch:
// the client of the factory's per-tick sandbox door (tick keh), configured
// from the dispatch the reconciler already assembled — the door's required
// fields (epic, base ref, title, attempt) ride the Dispatch precisely so no
// executor re-derives facts the run already knows, and the repository the
// collect reads is the orchestrator's own checkout, which is what the Go
// side collects from (the decided placement, tick xev).
//
// The factory's base URL and the run's own gateway token come from the
// environment — TICKS_FACTORY_URL and TICKS_FACTORY_TOKEN, which a container
// boot exports for the orchestrator and an operator exports on a laptop
// driving cloud workers — and a missing either is a dispatch that fails
// BEFORE the tick is claimed, because the executor's constructor refuses it.
// The substrate is the zero value, stated deliberately: the door is an HTTP
// route but carries no versioned protocol the client could pin a floor
// against, and provenance records null rather than a number nobody checks.
func sandboxExecutor(d reconcile.Dispatch) (reconcile.Executor, reconcile.Substrate, error) {
	executor, err := cloudflaresandbox.New(cloudflaresandbox.Options{
		FactoryURL: os.Getenv("TICKS_FACTORY_URL"),
		Token:      os.Getenv("TICKS_FACTORY_TOKEN"),
		RunID:      d.RunID,
		EpicID:     d.EpicID,
		BaseRef:    d.BaseRef,
		Title:      d.Title,
		// The model, the harness and the rendered role prompt the profile
		// resolved all cross the door (ticks a08, 9iz): the worker is booted
		// on the model, bound to the harness and delivered the prompt, and the
		// handle names the model and the harness back — so what the dispatch
		// records is what ran.
		Model:   d.Profile.Model,
		Harness: d.Profile.Runner,
		Prompt:  d.Profile.Prompt,
		// A carried dispatch's work base (epic hn6, run_3f034e68): the
		// container measures the carried work from it, so a worker that found
		// it complete and added nothing settles succeeded, not no-work.
		WorkBaseSHA: d.WorkBaseSHA,
		Attempt:     d.Attempt,
		StateDir:    d.StateDir,
		Repo:        d.Repo,
		Remote:      d.Remote,
	})
	if err != nil {
		return nil, reconcile.Substrate{}, err
	}
	return executor, reconcile.Substrate{}, nil
}

// pushInterval is the local executor's push cadence, as run-epic always passed
// it: a worker's in-progress work is durable without waiting for a settle.
const pushInterval = 60 * time.Second

// herdrExecutor builds the herdr executor for one dispatch. It compiles the
// agent argv BEFORE dialling herdr — a routing refusal costs zero dials and
// leaves no half-made workspace behind — and reports the substrate the
// handshake observed, which the reconciler states in the dispatch's
// provenance.
func herdrExecutor(gate string, d reconcile.Dispatch) (reconcile.Executor, reconcile.Substrate, error) {
	cfg, argv, err := spawnArgv(gate, d)
	if err != nil {
		return nil, reconcile.Substrate{}, err
	}
	// The socket the runners configuration resolves — orchestration.socket,
	// $HERDR_SOCKET_PATH, then the default — so a repository that pins a
	// socket is dialled at the one it names.
	socket, err := runconfig.ResolveSocket(cfg)
	if err != nil {
		return nil, reconcile.Substrate{}, err
	}
	executor, err := herdr.New(herdr.Options{
		Repo:       d.Repo,
		StateDir:   d.StateDir,
		SocketPath: socket,
		Kind:       d.Profile.Runner,
		Args:       argv,
		Model:      d.Profile.Model,
		RolePrompt: d.Profile.Prompt,
		Remote:     d.Remote,
		Attempt:    d.Attempt,
		// The gateway metering join (tick dm2): a pi dispatch on a Workers AI
		// model gets its calls tagged with the run id and routed through the
		// operator's AI Gateway, so the status model can meter the local
		// spend from the gateway's own logs. Nil on a host whose ~/.ticfacrc
		// names no gateway or no token — the documented optional-telemetry
		// state, and the dispatch then runs exactly as it did before.
		Metering: herdrMetering(d),
		// What the tick's earlier attempts found (tick nvn), for the same
		// section of the worker prompt the local executor renders.
		PriorReports: d.PriorReports,
		// What the tick's earlier attempts left PRESERVED (tick pbb), for the
		// same section of the worker prompt the local executor renders.
		PriorSnapshots: d.PriorSnapshots,
		// The earlier attempt that stopped to ask (tick tyd).
		Escalation: d.Escalation,
		// The stuck watch's window (tick wv2), the run's.
		StuckAfter: d.StuckAfter,
	})
	if err != nil {
		return nil, reconcile.Substrate{}, err
	}
	info := executor.ServerInfo()
	return executor, reconcile.Substrate{Protocol: int(info.Protocol), ServerVersion: info.Version}, nil
}

// spawnArgv compiles the agent argv for a herdr dispatch from the runners.toml
// the run reads its gate from. The KIND and the MODEL come off the profile —
// the profile is what the dispatch's records name, so what launches must be
// what they say — while the EFFORT and the escape-hatch ARGS come off the
// roles table the profile itself was routed through, which is the only place
// they live. A repository that routes nothing compiles the profile's kind and
// model alone.
//
// gate is the runners.toml path, and an EMPTY one is the dispatch's own
// repository's .tick/runners.toml — the same default the reconciler applies
// to its GateConfig (tick 53k). The factory that calls here is built BEFORE
// the reconciler defaults anything, and `ticfac run` names no --gate at all,
// so the empty path is the DEFAULT path: read literally it is ENOENT, "routes
// nothing", and a herdr pane that drops the roles table's effort and args and
// dials the default socket even when orchestration.socket is where the run
// detected herdr. Defaulting it here, at the dispatch, keeps the factory and
// the reconciler reading one file rather than agreeing by luck.
func spawnArgv(gate string, d reconcile.Dispatch) (*runconfig.Config, []string, error) {
	w := runconfig.Worker{Role: d.Role, Kind: d.Profile.Runner, Model: d.Profile.Model}
	if gate == "" {
		gate = filepath.Join(d.Repo, filepath.FromSlash(runconfig.FileName))
	}
	// A herdr dispatch runs on this machine, so runners.local.toml merges
	// over the common file (tick 5uo) — its tiers are real here.
	cfg, err := runconfig.LoadFor(gate, runconfig.SubstrateHerdr)
	if err != nil && !os.IsNotExist(err) {
		// A MISSING file routes nothing — the profile ships as written. A
		// file that EXISTS and fails validation is a stop, never a silent
		// spawn without the effort and args it was supposed to carry.
		return nil, nil, err
	}
	if cfg != nil {
		for _, candidate := range profile.RunnersRoleCandidates(d.Role) {
			if entry, ok := cfg.Roles[candidate]; ok && entry != nil {
				resolved, resolveErr := cfg.ResolveOn(runconfig.SubstrateHerdr, candidate, runconfig.Tier(d.Tier))
				if resolveErr != nil {
					return nil, nil, resolveErr
				}
				// The roles table contributed effort and args only: the kind
				// and the model are the profile's, and must stay so — a
				// launch flag that disagreed with provenance would be
				// provenance that lies.
				w.Effort, w.Args = resolved.Effort, resolved.Args
				break
			}
		}
	}
	spawn, err := runconfig.Compile(w, cfg.FullAuto(), runconfig.SpawnContext{
		// The resolvers, not the values: only a kind whose row needs the git
		// common dir (codex, in a linked worktree) or pi's model catalog pays
		// for them, and a worker whose kind needs neither never fails on a
		// probe it would never have used.
		ResolveGitCommonDir: func() (string, error) {
			out, err := exec.Command("git", "-C", d.Repo, "rev-parse", "--git-common-dir").Output()
			if err != nil {
				return "", fmt.Errorf("resolve the git common dir of %s: %w", d.Repo, err)
			}
			return strings.TrimSpace(string(out)), nil
		},
		ResolvePiCatalog: func() (*runconfig.PiCatalog, error) {
			out, err := exec.Command("pi", "--list-models").Output()
			if err != nil {
				return nil, fmt.Errorf("read pi's own model catalog (`pi --list-models`): %w", err)
			}
			return runconfig.ParsePiCatalog(strings.NewReader(string(out)))
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("compile the herdr spawn for %s (%s): %w", d.TickID, w.Label(), err)
	}
	return cfg, spawn.Argv, nil
}

// herdrMetering resolves the gateway metering join for one herdr dispatch
// (tick dm2): the run id the spend is attributed to, and the operator's AI
// Gateway URL — both from facts the dispatch and the host already hold. Nil,
// never an error, on a host that cannot join: cost telemetry is the
// documented OPTIONAL state (gatewaytrace.ConfigFrom's own refusal names the
// command that fixes it), a dispatch must never stop over it, and the cost
// line says "not metered" honestly instead.
//
// BOTH halves must exist or nothing is built: the gateway URL without the
// token would send requests the gateway refuses (the join would break the
// dispatch it meant to meter), and the token without the gateway names a
// route nothing reads. A half-configured factory is the same half-set state
// the jev credential resolution names rather than guesses around.
func herdrMetering(d reconcile.Dispatch) *subprocess.GatewayMetering {
	file, err := credentials.Load()
	if err != nil {
		return nil
	}
	gateway := strings.TrimSpace(file.Get(credentials.KeyGatewayURL))
	token := strings.TrimSpace(file.Get(credentials.KeyCloudflareAPIToken))
	if gateway == "" || token == "" {
		return nil
	}
	if _, _, ok := gatewaytrace.GatewayIDs(gateway); !ok {
		// A gateway not hosted by Cloudflare has no logs API to join to; the
		// requests would still route through it, but nothing could ever read
		// the spend back, and routing without the read is cost without the
		// metering this join exists for.
		return nil
	}
	return &subprocess.GatewayMetering{RunID: d.RunID, GatewayURL: gateway}
}
