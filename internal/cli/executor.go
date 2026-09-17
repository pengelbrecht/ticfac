// The executor factory a production run uses: the routing tick to1 (epic av8)
// put behind `ticfac run-epic`. A dispatch goes through the executor its
// RESOLVED PROFILE names — the reconciler asks, this side answers, because a
// name the reconciler cannot spell (herdr) is a name the reconciler must not
// spell: internal/reconcile carries no herdr code by design, and the seam test
// in internal/exec/herdr enforces it.
//
// The honoured set is stated here too, as the KnownExecutors the reconciler's
// construction-time checks admit: the local subprocess executor this build
// always has, and the herdr executor, with the agent kinds it can launch.
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/herdr"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
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
// the same validated reader everything else uses. runner is the operator's
// fallback for a dispatch whose profile resolved none.
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
		default:
			// Unreachable in a run: usableProfile refused any profile naming an
			// executor outside knownExecutors() at construction. Refused again
			// here anyway, fail closed, because a factory that silently fell
			// back would dispatch through an executor the record does not name.
			return nil, reconcile.Substrate{}, fmt.Errorf(
				"the profile for %s names executor %q, which this build can honour neither of %s",
				d.TickID, d.Profile.Executor, subprocess.ExecutorName+", "+herdr.ExecutorName)
		}
	}
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
		// What the tick's earlier attempts found (tick nvn), for the same
		// section of the worker prompt the local executor renders.
		PriorReports: d.PriorReports,
		// What the tick's earlier attempts left PRESERVED (tick pbb), for the
		// same section of the worker prompt the local executor renders.
		PriorSnapshots: d.PriorSnapshots,
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
func spawnArgv(gate string, d reconcile.Dispatch) (*runconfig.Config, []string, error) {
	w := runconfig.Worker{Role: d.Role, Kind: d.Profile.Runner, Model: d.Profile.Model}
	cfg, err := runconfig.Load(gate)
	if err != nil && !os.IsNotExist(err) {
		// A MISSING file routes nothing — the profile ships as written. A
		// file that EXISTS and fails validation is a stop, never a silent
		// spawn without the effort and args it was supposed to carry.
		return nil, nil, err
	}
	if cfg != nil {
		for _, candidate := range profile.RunnersRoleCandidates(d.Role) {
			if entry, ok := cfg.Roles[candidate]; ok && entry != nil {
				resolved, resolveErr := cfg.Resolve(candidate, runconfig.Tier(d.Tier))
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
