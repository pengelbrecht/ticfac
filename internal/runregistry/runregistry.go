// Package runregistry is the machine's own record of where each run works —
// the one probing convention every surface that reads MORE THAN ONE checkout
// settles on (ticfac tick aj9).
//
// THE PROBLEM IT SETTLES. A local run's liveness is per-checkout: run.pid
// lives in the repository the run works in ([runlife.Dir]), and a probe from
// anywhere else answers not_running — a live run read dead from a second
// checkout of the same repository, and the status model's dead-run wait with
// it. For `ticfac status <run-id>` that answer is correct and stays: the
// model is a snapshot of what the checkout it runs in can read, and --repo
// names another. But a surface that LISTS runs — the bare `ticfac` listing
// (tick 2qz), which reads runs from many checkouts at once — cannot take the
// per-checkout answer: it would hand the person a dead-run wait and a resume
// command for a run that is already running.
//
// THE CONVENTION. Every local run registers the checkout it works in, on the
// machine, at the moment it claims its life ([runlife.Claim] writes it), and
// a surface that probes a run from anywhere on this machine reads the run's
// machine-local facts — its pidfile, its feed, its standing worktrees — at
// the registered repo. WorkingRepo is the convention as one call: the
// registered repo when the machine holds one, else the checkout the caller is
// in, which is the per-checkout answer status gives today. With no
// registration the convention claims nothing wider than that fallback.
//
// WHY A REGISTRATION AND NOT THE RUN-STATE STORE'S ORIGIN VIEW. The run's
// durable records are on origin and readable from every checkout — but
// liveness is not durable: run.pid is exhaust, never pushed, and origin can
// only say a run exists, not whether its process stands. The only way origin
// could answer is the run writing its working-repo path into a pushed record,
// and this is a public repository where nothing operator-specific is ever
// committed (.tick/config.md) — a pushed home path identifies the operator —
// while a path is host-local besides: from another machine it names nothing.
// Liveness is a machine fact, so it is registered on the machine, beside the
// executor state under ~/.ticfac that already answers for attempts.
//
// The registration is written atomically and is last-writer-wins: a run
// resumed in another checkout overwrites its own earlier registration,
// because the last claim is the run that is driving. Release does NOT remove
// it: a finished run stays enumerable — its own terminal records answer what
// its absent pidfile cannot — and only a run id claimed again elsewhere moves
// the answer.
package runregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SchemaVersion is the registration's version. A reader that meets a value
// it does not know refuses the record rather than guessing at it — the rule
// every versioned surface here holds.
const SchemaVersion = 1

// RegistryDirEnv names, in the environment, the directory registrations live
// in — the same override the executor state takes ($TICFAC_EXEC_STATE_DIR),
// for tests and containers that must not write on the operator's home.
const RegistryDirEnv = "TICFAC_REGISTRY_DIR"

// Registration is one run's record: where it works, on which machine, since
// when.
type Registration struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	// Repo is the ABSOLUTE path of the checkout the run works in — the one
	// place its pidfile, its feed and its worktrees can be read.
	Repo string `json:"repo"`
	// Host names the machine that wrote the registration: a path is
	// host-local, and the name is what says whose.
	Host string `json:"host"`
	// RegisteredAt is when the run claimed its life there, RFC3339.
	RegisteredAt string `json:"registered_at"`
}

// Dir is where registrations live: $TICFAC_REGISTRY_DIR when set, else
// ~/.ticfac/registry — machine-local state, never inside a checkout, which is
// the very thing a registration names.
func Dir() string {
	if dir := strings.TrimSpace(os.Getenv(RegistryDirEnv)); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "ticfac", "registry")
	}
	return filepath.Join(home, ".ticfac", "registry")
}

// checkID refuses a run id that would escape the registry: run ids reach this
// package from the command line, and one carrying a separator — or a dot of
// traversal — would write outside the registry directory. The same rules
// runstate holds for the record names it builds.
func checkID(runID string) error {
	switch {
	case runID == "":
		return errors.New("run id is empty")
	case strings.ContainsAny(runID, `/\`):
		return fmt.Errorf("run id %q contains a path separator", runID)
	case runID == "." || runID == "..":
		return fmt.Errorf("run id %q is a path traversal", runID)
	case strings.HasPrefix(runID, "."):
		return fmt.Errorf("run id %q starts with a dot", runID)
	}
	return nil
}

// Register writes one run's registration: the checkout it works in, on this
// machine, atomically — a reader mid-write sees the whole registration or
// none of it. A run resumed in another checkout overwrites its own earlier
// registration: the last claim is the run that is driving.
func Register(runID, repo string) error {
	return write(Dir(), runID, repo, time.Now())
}

// write is Register against a named directory, so tests hold the directory
// without the environment.
func write(dir, runID, repo string, now time.Time) error {
	if err := checkID(runID); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	// Absolute, because a relative path is a coordinate of the process that
	// wrote it and the registration exists to be read from OTHER checkouts.
	abs, err := filepath.Abs(repo)
	if err != nil {
		return fmt.Errorf("resolve %q to an absolute path: %w", repo, err)
	}
	host, _ := os.Hostname()
	reg := Registration{
		SchemaVersion: SchemaVersion,
		RunID:         runID,
		Repo:          abs,
		Host:          host,
		RegisteredAt:  now.UTC().Format(time.RFC3339),
	}
	return writeFile(dir, reg)
}

// writeFile puts one registration in place with a rename, so no reader ever
// sees half of it — the same guarantee run.pid holds.
func writeFile(dir string, reg Registration) error {
	raw, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, reg.RunID+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, reg.RunID+".json"))
}

// Lookup reads one run's registration. The second return says whether the
// machine holds one; a registration that exists but will not decode is an
// error, never a silent absence.
func Lookup(runID string) (Registration, bool, error) {
	return read(Dir(), runID)
}

// read is Lookup against a named directory.
func read(dir, runID string) (Registration, bool, error) {
	if err := checkID(runID); err != nil {
		return Registration{}, false, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, runID+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return Registration{}, false, nil
	}
	if err != nil {
		return Registration{}, false, err
	}
	var reg Registration
	if err := json.Unmarshal(raw, &reg); err != nil {
		return Registration{}, false, fmt.Errorf("decode the registration for %s: %w", runID, err)
	}
	return reg, true, nil
}

// WorkingRepo is the probing convention as one call: the repo the run is
// registered to work in on this machine, else the fallback — the checkout
// the caller is in, which is the per-checkout answer `ticfac status` gives
// today. The second return says whether a registration named the repo, so a
// caller that wants to state the difference can.
func WorkingRepo(runID, fallback string) (string, bool) {
	return workingRepo(Dir(), runID, fallback)
}

// workingRepo is WorkingRepo against a named directory. A registration that
// cannot be read falls back rather than failing the probe: the convention
// narrows the question where it can and never widens a claim it cannot.
func workingRepo(dir, runID, fallback string) (string, bool) {
	reg, ok, err := read(dir, runID)
	if err != nil || !ok || reg.Repo == "" {
		return fallback, false
	}
	return reg.Repo, true
}

// List is every registration this machine holds, by run id — the enumeration
// a listing surface starts from. An absent registry is an empty list, not an
// error, and a registration that cannot be decoded is an error, not a skip: a
// listing that quietly drops a run is the silence this package exists to end.
func List() ([]Registration, error) {
	return list(Dir())
}

// list is List against a named directory.
func list(dir string) ([]Registration, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Registration{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Registration{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var reg Registration
		if err := json.Unmarshal(raw, &reg); err != nil {
			return nil, fmt.Errorf("decode %s: %w", entry.Name(), err)
		}
		out = append(out, reg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RunID < out[j].RunID })
	return out, nil
}
