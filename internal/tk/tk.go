// Package tk is the only tracker client used by ticfac.
//
// The command surface is deliberately driven by contracts/tk-json-manifest.json:
// command arguments come from that manifest, every subprocess is pinned to
// JSON contract 1, and JSON is validated against the published schema before
// it is decoded into a Go value. This keeps a tracker upgrade from silently
// changing what the reconciler believes it read.
package tk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/schema"
)

const (
	// Contract is the tk JSON contract this package was built against.
	Contract = 1

	// ContractEnv is the environment variable tk accepts for pinning a JSON
	// contract. It works for every manifest command, including merge drivers.
	ContractEnv = "TK_JSON_CONTRACT"

	// ContractEnvValue is Contract in the spelling expected by tk.
	ContractEnvValue = "1"

	// ExitUnsupportedContract is tk's fail-closed exit code when it cannot
	// serve the requested JSON contract.
	ExitUnsupportedContract = 11

	// ExitDispatchWidth is tk's refusal when a claim would exceed the
	// configured dispatch width.
	ExitDispatchWidth = 8

	contractEnv      = ContractEnv
	contractEnvValue = ContractEnvValue
)

// Request is one invocation sent to the injected command runner. Env contains
// environment overrides rather than a complete environment; the real runner
// starts with the process environment and applies these values.
type Request struct {
	Binary string
	Dir    string
	Args   []string
	Env    map[string]string
}

// Result is the result returned by a command runner. ExitCode is the process
// exit status; a negative value means the process could not be started or did
// not provide a usable status.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Err      error
}

// Runner makes subprocess execution injectable for unit tests. Production
// clients use the standard-library runner installed by New.
type Runner interface {
	Run(context.Context, Request) Result
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(context.Context, Request) Result

func (f RunnerFunc) Run(ctx context.Context, request Request) Result {
	return f(ctx, request)
}

// Options configures a Client.
type Options struct {
	// Binary is the tk executable. Empty means "tk" and therefore uses PATH.
	Binary string
	// Dir is the fixture or repository in which tk runs. Empty inherits the
	// current working directory.
	Dir string
	// Env contains additional environment overrides. ContractEnv is always
	// overwritten with ContractEnvValue.
	Env map[string]string
	// Runner replaces subprocess execution. It is intended for unit tests.
	Runner Runner
	// ContractsDir locates the vendored contract bundle. Empty discovers it
	// from the repository root, as internal/contracts does elsewhere.
	ContractsDir string
}

// Client is a contract-pinned tk client. New probes tk once with version and
// caches that response; all subsequent calls use the cached startup check.
type Client struct {
	binary   string
	dir      string
	env      map[string]string
	runner   Runner
	manifest *manifest
	version  VersionInfo
}

// New creates a client and performs the one startup version check required by
// the manifest. A missing binary, an unsupported contract, an invalid version
// response, or a tk version below the manifest minimum is returned here before
// any caller can issue a tracker write.
func New(options Options) (*Client, error) {
	return NewContext(context.Background(), options)
}

// NewContext is New with a caller-provided context for the startup probe.
func NewContext(ctx context.Context, options Options) (*Client, error) {
	m, err := loadManifest(options.ContractsDir)
	if err != nil {
		return nil, err
	}

	if options.Binary == "" {
		options.Binary = "tk"
	}
	if options.Runner == nil {
		options.Runner = execRunner{}
	}

	c := &Client{
		binary:   options.Binary,
		dir:      options.Dir,
		env:      cloneEnv(options.Env),
		runner:   options.Runner,
		manifest: m,
	}

	var version VersionInfo
	if err := c.invokeJSON(ctx, "version", nil, &version); err != nil {
		return nil, err
	}
	if err := c.validateStartupVersion(version); err != nil {
		return nil, err
	}
	c.version = version
	return c, nil
}

// NewClient is an explicit synonym for New for callers that prefer a
// constructor name matching the returned type.
func NewClient(options Options) (*Client, error) {
	return New(options)
}

// Version returns the version response captured during startup. It does not
// invoke tk again; the minimum-version check is intentionally once per client.
func (c *Client) Version(_ context.Context) (VersionInfo, error) {
	return c.version, nil
}

// Show returns one tick by id.
func (c *Client) Show(ctx context.Context, tickID string) (Tick, error) {
	var out Tick
	err := c.invokeJSON(ctx, "show", map[string]string{"<tick-id>": tickID}, &out)
	return out, err
}

// List returns all ticks published by tk.
func (c *Client) List(ctx context.Context) (TickList, error) {
	var out TickList
	err := c.invokeJSON(ctx, "list", nil, &out)
	return out, err
}

// Ready returns all ticks ready for work.
func (c *Client) Ready(ctx context.Context) (TickList, error) {
	var out TickList
	err := c.invokeJSON(ctx, "ready", nil, &out)
	return out, err
}

// Next returns the next actionable tick, or nil when tk reports no next tick.
func (c *Client) Next(ctx context.Context) (*Tick, error) {
	var out *Tick
	err := c.invokeJSON(ctx, "next", nil, &out)
	return out, err
}

// Deps returns the dependencies and dependants of a tick.
func (c *Client) Deps(ctx context.Context, tickID string) (Dependencies, error) {
	var out Dependencies
	err := c.invokeJSON(ctx, "deps", map[string]string{"<tick-id>": tickID}, &out)
	return out, err
}

// Graph returns the orchestration graph rooted at an epic.
func (c *Client) Graph(ctx context.Context, epicID string) (Graph, error) {
	var out Graph
	err := c.invokeJSON(ctx, "graph", map[string]string{"<epic-id>": epicID}, &out)
	return out, err
}

// Status returns the working-tree changes known to tk.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var out Status
	err := c.invokeJSON(ctx, "status", nil, &out)
	return out, err
}

// Claim marks a tick in_progress and assigns it to owner.
func (c *Client) Claim(ctx context.Context, tickID, owner string) (Tick, error) {
	var out Tick
	err := c.invokeJSON(ctx, "claim", map[string]string{
		"<tick-id>": tickID,
		"<owner>":   owner,
	}, &out)
	return out, err
}

// Update replaces the notes on a tick with text.
func (c *Client) Update(ctx context.Context, tickID, text string) (Tick, error) {
	var out Tick
	err := c.invokeJSON(ctx, "update", map[string]string{
		"<tick-id>": tickID,
		"<text>":    text,
	}, &out)
	return out, err
}

// Note appends a note to a tick.
func (c *Client) Note(ctx context.Context, tickID, text string) (Tick, error) {
	var out Tick
	err := c.invokeJSON(ctx, "note", map[string]string{
		"<tick-id>": tickID,
		"<text>":    text,
	}, &out)
	return out, err
}

// Close closes a tick.
func (c *Client) Close(ctx context.Context, tickID string) (Tick, error) {
	var out Tick
	err := c.invokeJSON(ctx, "close", map[string]string{"<tick-id>": tickID}, &out)
	return out, err
}

// Reopen reopens a closed tick.
func (c *Client) Reopen(ctx context.Context, tickID string) (Tick, error) {
	var out Tick
	err := c.invokeJSON(ctx, "reopen", map[string]string{"<tick-id>": tickID}, &out)
	return out, err
}

// MergeFile runs tk's file merge driver. A successful merge returns nil; tk's
// exit code 1 is returned as a typed ExitError so a Git driver can preserve
// the distinction from a contract or dispatch refusal.
func (c *Client) MergeFile(ctx context.Context, base, ours, theirs, path string) error {
	return c.invokeExit(ctx, "merge-file", map[string]string{
		"<base>":   base,
		"<ours>":   ours,
		"<theirs>": theirs,
		"<path>":   path,
	})
}

// MergeActivity runs tk's activity-log merge driver.
func (c *Client) MergeActivity(ctx context.Context, ancestor, current, other, path string) error {
	return c.invokeExit(ctx, "merge-activity", map[string]string{
		"<ancestor>": ancestor,
		"<current>":  current,
		"<other>":    other,
		"<path>":     path,
	})
}

func (c *Client) invokeJSON(ctx context.Context, id string, values map[string]string, into any) error {
	command, args, err := c.command(id, values)
	if err != nil {
		return err
	}
	result := c.runner.Run(ctx, c.request(args))
	if err := classifyResult(command, args, result, c.manifest); err != nil {
		return err
	}

	var document any
	if err := json.Unmarshal(result.Stdout, &document); err != nil {
		return &ResponseError{
			Command:  command.ID,
			Problems: []string{fmt.Sprintf("invalid JSON: %v", err)},
			Output:   append([]byte(nil), result.Stdout...),
		}
	}
	problems := schema.Validate(command.Schema, c.manifest.Defs, document)
	if len(problems) != 0 {
		return &ResponseError{
			Command:  command.ID,
			Problems: problems,
			Output:   append([]byte(nil), result.Stdout...),
		}
	}
	if into == nil {
		return nil
	}
	if err := json.Unmarshal(result.Stdout, into); err != nil {
		return &ResponseError{
			Command:  command.ID,
			Problems: []string{fmt.Sprintf("decode response: %v", err)},
			Output:   append([]byte(nil), result.Stdout...),
		}
	}
	return nil
}

func (c *Client) invokeExit(ctx context.Context, id string, values map[string]string) error {
	command, args, err := c.command(id, values)
	if err != nil {
		return err
	}
	result := c.runner.Run(ctx, c.request(args))
	return classifyResult(command, args, result, c.manifest)
}

func (c *Client) request(args []string) Request {
	return Request{
		Binary: c.binary,
		Dir:    c.dir,
		Args:   append([]string(nil), args...),
		Env:    cloneEnv(c.env),
	}
}

func (c *Client) command(id string, values map[string]string) (manifestCommand, []string, error) {
	command, ok := c.manifest.Commands[id]
	if !ok {
		return manifestCommand{}, nil, fmt.Errorf("tk manifest has no command %q", id)
	}
	args := make([]string, len(command.Argv))
	for i, arg := range command.Argv {
		if value, ok := values[arg]; ok {
			args[i] = value
			continue
		}
		if strings.HasPrefix(arg, "<") && strings.HasSuffix(arg, ">") {
			return manifestCommand{}, nil, fmt.Errorf("tk manifest command %q has unbound argument %q", id, arg)
		}
		args[i] = arg
	}
	return command, args, nil
}

func (c *Client) validateStartupVersion(version VersionInfo) error {
	if !containsInt(version.SupportedContracts, c.manifest.Contract) {
		return &ErrUnsupportedContract{
			Command:   "version",
			Requested: c.manifest.Contract,
			Supported: append([]int(nil), version.SupportedContracts...),
			ExitCode:  ExitUnsupportedContract,
		}
	}
	if err := compareMinimumVersion(version.Tk, c.manifest.MinTkVersion); err != nil {
		return err
	}
	return nil
}

func loadManifest(directory string) (*manifest, error) {
	if directory == "" {
		var err error
		directory, err = contracts.Dir()
		if err != nil {
			return nil, fmt.Errorf("locate tk JSON manifest: %w", err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(directory, "tk-json-manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("read tk JSON manifest: %w", err)
	}

	var published struct {
		Comment            string `json:"$comment"`
		Contract           int    `json:"contract"`
		SupportedContracts []int  `json:"supported_contracts"`
		MinTkVersion       string `json:"min_tk_version"`
		Request            struct {
			Comment             string `json:"$comment"`
			Flag                string `json:"flag"`
			Env                 string `json:"env"`
			Placement           string `json:"placement"`
			UnsupportedExitCode int    `json:"unsupported_exit_code"`
			UnsupportedBehavior string `json:"unsupported_behavior"`
		} `json:"request"`
		Hosts struct {
			Comment     string `json:"$comment"`
			RunsTk      string `json:"runs_tk"`
			CannotRunTk string `json:"cannot_run_tk"`
			Proof       string `json:"proof"`
		} `json:"hosts"`
		Defs     map[string]json.RawMessage `json:"$defs"`
		Commands []struct {
			ID          string            `json:"id"`
			Command     string            `json:"command"`
			Kind        string            `json:"kind"`
			Argv        []string          `json:"argv"`
			Output      string            `json:"output"`
			Since       int               `json:"since"`
			Description string            `json:"description"`
			Schema      json.RawMessage   `json:"schema"`
			ExitCodes   map[string]string `json:"exit_codes"`
		} `json:"commands"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&published); err != nil {
		return nil, fmt.Errorf("parse tk JSON manifest: %w", err)
	}
	if published.Contract != Contract {
		return nil, fmt.Errorf("tk JSON manifest contract = %d, want %d", published.Contract, Contract)
	}
	if published.Request.Env != ContractEnv {
		return nil, fmt.Errorf("tk JSON manifest contract environment = %q, want %q", published.Request.Env, ContractEnv)
	}
	if published.Request.UnsupportedExitCode != ExitUnsupportedContract {
		return nil, fmt.Errorf("tk JSON manifest unsupported exit code = %d, want %d", published.Request.UnsupportedExitCode, ExitUnsupportedContract)
	}
	if !containsInt(published.SupportedContracts, Contract) {
		return nil, fmt.Errorf("tk JSON manifest supported_contracts %v does not include %d", published.SupportedContracts, Contract)
	}
	if published.MinTkVersion == "" {
		return nil, errors.New("tk JSON manifest has no minimum tk version")
	}
	defs, err := schema.ParseDefs(published.Defs)
	if err != nil {
		return nil, fmt.Errorf("parse tk JSON manifest definitions: %w", err)
	}

	m := &manifest{
		Contract:           published.Contract,
		SupportedContracts: append([]int(nil), published.SupportedContracts...),
		MinTkVersion:       published.MinTkVersion,
		Defs:               defs,
		Commands:           make(map[string]manifestCommand, len(published.Commands)),
	}
	for _, command := range published.Commands {
		if command.ID == "" {
			return nil, errors.New("tk JSON manifest contains a command with no id")
		}
		if _, exists := m.Commands[command.ID]; exists {
			return nil, fmt.Errorf("tk JSON manifest repeats command %q", command.ID)
		}
		if len(command.Argv) == 0 || command.Argv[0] != command.Command {
			return nil, fmt.Errorf("tk JSON manifest command %q has inconsistent argv", command.ID)
		}
		entry := manifestCommand{
			ID:        command.ID,
			Command:   command.Command,
			Kind:      command.Kind,
			Argv:      append([]string(nil), command.Argv...),
			Output:    command.Output,
			ExitCodes: cloneStrings(command.ExitCodes),
		}
		if command.Output == "json" {
			if len(command.Schema) == 0 || string(command.Schema) == "null" {
				return nil, fmt.Errorf("tk JSON manifest command %q has no response schema", command.ID)
			}
			entry.Schema, err = schema.ParseSchema(command.Schema)
			if err != nil {
				return nil, fmt.Errorf("parse tk JSON manifest schema for %q: %w", command.ID, err)
			}
		} else if command.Output == "exit-code" {
			if len(command.ExitCodes) == 0 || len(command.Schema) != 0 && string(command.Schema) != "null" {
				return nil, fmt.Errorf("tk JSON manifest command %q has invalid exit-code declaration", command.ID)
			}
		} else {
			return nil, fmt.Errorf("tk JSON manifest command %q has unsupported output %q", command.ID, command.Output)
		}
		m.Commands[command.ID] = entry
	}

	for _, id := range requiredCommands {
		if _, ok := m.Commands[id]; !ok {
			return nil, fmt.Errorf("tk JSON manifest is missing command %q", id)
		}
	}
	if len(m.Commands) != len(requiredCommands) {
		return nil, fmt.Errorf("tk JSON manifest publishes %d commands, want %d", len(m.Commands), len(requiredCommands))
	}
	return m, nil
}

func classifyResult(command manifestCommand, args []string, result Result, m *manifest) error {
	if result.Err != nil && result.ExitCode < 0 {
		return &CommandError{
			Command:  command.ID,
			Args:     append([]string(nil), args...),
			ExitCode: result.ExitCode,
			Stderr:   strings.TrimSpace(string(result.Stderr)),
			Err:      result.Err,
		}
	}
	switch result.ExitCode {
	case 0:
		if result.Err != nil {
			return &CommandError{Command: command.ID, Args: append([]string(nil), args...), ExitCode: 0, Stderr: strings.TrimSpace(string(result.Stderr)), Err: result.Err}
		}
		return nil
	case ExitUnsupportedContract:
		return &ErrUnsupportedContract{
			Command:   command.ID,
			Requested: m.Contract,
			Supported: append([]int(nil), m.SupportedContracts...),
			ExitCode:  ExitUnsupportedContract,
			Stderr:    strings.TrimSpace(string(result.Stderr)),
		}
	case ExitDispatchWidth:
		return &ErrDispatchWidth{
			Command:  command.ID,
			ExitCode: ExitDispatchWidth,
			Stderr:   strings.TrimSpace(string(result.Stderr)),
		}
	default:
		return &ExitError{
			Command:  command.ID,
			Args:     append([]string(nil), args...),
			ExitCode: result.ExitCode,
			Stderr:   strings.TrimSpace(string(result.Stderr)),
		}
	}
}

func compareMinimumVersion(actual, minimum string) error {
	if actual == "dev" {
		return nil
	}
	got, err := parseVersion(actual)
	if err != nil {
		return &ErrMinimumVersion{Required: minimum, Actual: actual, Err: err}
	}
	want, err := parseVersion(minimum)
	if err != nil {
		return fmt.Errorf("invalid minimum tk version %q: %w", minimum, err)
	}
	for i := range got {
		if got[i] < want[i] {
			return &ErrMinimumVersion{Required: minimum, Actual: actual}
		}
		if got[i] > want[i] {
			return nil
		}
	}
	return nil
}

func parseVersion(value string) ([3]int, error) {
	var result [3]int
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if dash := strings.IndexAny(value, "-+"); dash >= 0 {
		value = value[:dash]
	}
	parts := strings.Split(value, ".")
	if len(parts) != len(result) {
		return result, fmt.Errorf("want major.minor.patch")
	}
	for i, part := range parts {
		if part == "" {
			return result, fmt.Errorf("empty version component")
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return result, fmt.Errorf("invalid version component %q", part)
		}
		result[i] = n
	}
	return result, nil
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func cloneEnv(values map[string]string) map[string]string {
	result := make(map[string]string, len(values)+1)
	for key, value := range values {
		result[key] = value
	}
	result[ContractEnv] = ContractEnvValue
	return result
}

func cloneStrings(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, request Request) Result {
	cmd := exec.CommandContext(ctx, request.Binary, request.Args...)
	cmd.Dir = request.Dir
	cmd.Env = applyEnv(os.Environ(), request.Env)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := Result{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: 0,
		Err:      err,
	}
	if err == nil {
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	} else {
		result.ExitCode = -1
	}
	return result
}

func applyEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return append([]string(nil), base...)
	}
	result := make([]string, 0, len(base)+len(overrides))
	seen := make(map[string]bool, len(overrides))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if value, override := overrides[key]; override {
			if !seen[key] {
				result = append(result, key+"="+value)
				seen[key] = true
			}
			continue
		}
		if ok {
			result = append(result, entry)
		}
	}
	for key, value := range overrides {
		if !seen[key] {
			result = append(result, key+"="+value)
		}
	}
	return result
}

type manifest struct {
	Contract           int
	SupportedContracts []int
	MinTkVersion       string
	Defs               map[string]*schema.Schema
	Commands           map[string]manifestCommand
}

type manifestCommand struct {
	ID        string
	Command   string
	Kind      string
	Argv      []string
	Output    string
	Schema    *schema.Schema
	ExitCodes map[string]string
}

var requiredCommands = []string{
	"version", "show", "list", "ready", "next", "deps", "graph", "status",
	"claim", "update", "note", "close", "reopen", "merge-file", "merge-activity",
}

// VersionInfo is the response to tk version --json.
type VersionInfo struct {
	Tk                 string `json:"tk"`
	Contract           int    `json:"contract"`
	SupportedContracts []int  `json:"supported_contracts"`
	MinTkVersion       string `json:"min_tk_version"`
	Manifest           string `json:"manifest"`
}

// VersionResponse is an alias for callers that name command results by their
// wire-level role.
type VersionResponse = VersionInfo

// Tick is one record returned by tk. Optional manifest fields remain their Go
// zero value when tk omits them, which is the wire contract's representation
// of an empty optional field.
type Tick struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Description        string   `json:"description,omitempty"`
	Notes              string   `json:"notes,omitempty"`
	Status             string   `json:"status"`
	Priority           int      `json:"priority"`
	Type               string   `json:"type"`
	Owner              string   `json:"owner"`
	Labels             []string `json:"labels,omitempty"`
	BlockedBy          []string `json:"blocked_by,omitempty"`
	After              []string `json:"after,omitempty"`
	Parent             string   `json:"parent,omitempty"`
	TargetDate         string   `json:"target_date,omitempty"`
	Role               string   `json:"role,omitempty"`
	DiscoveredFrom     string   `json:"discovered_from,omitempty"`
	AcceptanceCriteria string   `json:"acceptance_criteria,omitempty"`
	DeferUntil         string   `json:"defer_until,omitempty"`
	ExternalRef        string   `json:"external_ref,omitempty"`
	TraceID            string   `json:"trace_id,omitempty"`
	Manual             bool     `json:"manual,omitempty"`
	BaseBranch         string   `json:"base_branch,omitempty"`
	Requires           string   `json:"requires,omitempty"`
	Awaiting           string   `json:"awaiting,omitempty"`
	Verdict            string   `json:"verdict,omitempty"`
	CreatedBy          string   `json:"created_by"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
	StartedAt          string   `json:"started_at,omitempty"`
	ClosedAt           string   `json:"closed_at,omitempty"`
	ClosedReason       string   `json:"closed_reason,omitempty"`
	Action             string   `json:"action,omitempty"`
}

// TickList is the envelope returned by list and ready.
type TickList struct {
	Ticks   []Tick   `json:"ticks"`
	Filters *Filters `json:"filters,omitempty"`
}

// Filters are the optional filters echoed by list-like tk responses.
type Filters struct {
	TitleContains string   `json:"title_contains,omitempty"`
	DescContains  string   `json:"desc_contains,omitempty"`
	NotesContains string   `json:"notes_contains,omitempty"`
	LabelAny      []string `json:"label_any,omitempty"`
}

// Dependencies is the response to deps.
type Dependencies struct {
	BlockedBy []string `json:"blocked_by"`
	Blocks    []Tick   `json:"blocks"`
}

// DepsResponse is an alias for Dependencies.
type DepsResponse = Dependencies

// Graph is the response to graph.
type Graph struct {
	Epic                GraphEpic     `json:"epic"`
	NeedsPlanning       bool          `json:"needs_planning"`
	MissingProcessTicks []string      `json:"missing_process_ticks"`
	UnjustifiedGates    []string      `json:"unjustified_gates"`
	Stats               GraphStats    `json:"stats"`
	Dispatch            GraphDispatch `json:"dispatch"`
	Waves               []GraphWave   `json:"waves"`
	CriticalPath        int           `json:"critical_path"`
}

// GraphResponse is an alias for Graph.
type GraphResponse = Graph

type GraphEpic struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type GraphStats struct {
	TotalTasks    int `json:"total_tasks"`
	WaveCount     int `json:"wave_count"`
	MaxParallel   int `json:"max_parallel"`
	ReadyForAgent int `json:"ready_for_agent"`
	AwaitingHuman int `json:"awaiting_human"`
	Deferred      int `json:"deferred"`
}

type GraphDispatch struct {
	MaxParallel int      `json:"max_parallel"`
	InFlight    int      `json:"in_flight"`
	InFlightIDs []string `json:"in_flight_ids"`
	Free        int      `json:"free"`
	Now         []string `json:"now"`
	Source      string   `json:"source,omitempty"`
}

type GraphWave struct {
	Wave     int         `json:"wave"`
	Parallel int         `json:"parallel"`
	Ready    bool        `json:"ready"`
	Tasks    []GraphTask `json:"tasks"`
}

// GraphTask is a task node in a graph response. Its optional fields follow
// the open schema in the manifest.
type GraphTask struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Description        string   `json:"description,omitempty"`
	AcceptanceCriteria string   `json:"acceptance_criteria,omitempty"`
	Priority           int      `json:"priority"`
	Type               string   `json:"type,omitempty"`
	Labels             []string `json:"labels,omitempty"`
	Status             string   `json:"status"`
	Role               string   `json:"role,omitempty"`
	BlockedBy          []string `json:"blocked_by,omitempty"`
	Blocks             []string `json:"blocks,omitempty"`
	Awaiting           string   `json:"awaiting,omitempty"`
	DeferredUntil      string   `json:"deferred_until,omitempty"`
	AgentReady         bool     `json:"agent_ready"`
	Owner              string   `json:"owner,omitempty"`
	Parent             string   `json:"parent,omitempty"`
	Verdict            string   `json:"verdict,omitempty"`
	Requires           string   `json:"requires,omitempty"`
	Manual             bool     `json:"manual,omitempty"`
	BaseBranch         string   `json:"base_branch,omitempty"`
	TargetDate         string   `json:"target_date,omitempty"`
	ExternalRef        string   `json:"external_ref,omitempty"`
	TraceID            string   `json:"trace_id,omitempty"`
	Notes              string   `json:"notes,omitempty"`
	DiscoveredFrom     string   `json:"discovered_from,omitempty"`
	CreatedAt          string   `json:"created_at,omitempty"`
	UpdatedAt          string   `json:"updated_at,omitempty"`
	StartedAt          string   `json:"started_at,omitempty"`
	ClosedAt           string   `json:"closed_at,omitempty"`
	ClosedReason       string   `json:"closed_reason,omitempty"`
	CreatedBy          string   `json:"created_by,omitempty"`
	After              []string `json:"after,omitempty"`
}

// Status is the response to status.
type Status struct {
	Changes []string `json:"changes"`
}

// StatusResponse is an alias for Status.
type StatusResponse = Status

// ErrUnsupportedContract is returned for tk exit 11 and for a successful
// version response that does not advertise contract 1. Callers can use
// errors.As to fail closed without parsing stderr.
type ErrUnsupportedContract struct {
	Command   string
	Requested int
	Supported []int
	ExitCode  int
	Stderr    string
}

func (e *ErrUnsupportedContract) Error() string {
	message := fmt.Sprintf("tk command %q does not support JSON contract %d (supported: %v)", e.Command, e.Requested, e.Supported)
	if e.Stderr != "" {
		message += ": " + e.Stderr
	}
	return message
}

// ErrDispatchWidth is returned for tk exit 8.
type ErrDispatchWidth struct {
	Command  string
	ExitCode int
	Stderr   string
}

func (e *ErrDispatchWidth) Error() string {
	message := fmt.Sprintf("tk command %q refused: dispatch width exceeded", e.Command)
	if e.Stderr != "" {
		message += ": " + e.Stderr
	}
	return message
}

// ErrDispatch is a short alias for ErrDispatchWidth.
type ErrDispatch = ErrDispatchWidth

// ErrMinimumVersion means the binary is older than the pinned manifest
// requires, or did not publish a parseable release version.
type ErrMinimumVersion struct {
	Required string
	Actual   string
	Err      error
}

func (e *ErrMinimumVersion) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("tk version %q cannot satisfy minimum %q: %v", e.Actual, e.Required, e.Err)
	}
	return fmt.Sprintf("tk version %q is below minimum %q", e.Actual, e.Required)
}

func (e *ErrMinimumVersion) Unwrap() error { return e.Err }

// ExitError is a non-zero tk exit other than a typed contract or dispatch
// refusal.
type ExitError struct {
	Command  string
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *ExitError) Error() string {
	message := fmt.Sprintf("tk command %q exited with code %d", e.Command, e.ExitCode)
	if e.Stderr != "" {
		message += ": " + e.Stderr
	}
	return message
}

// CommandError means the process could not be started or its successful
// result was accompanied by a runner error.
type CommandError struct {
	Command  string
	Args     []string
	ExitCode int
	Stderr   string
	Err      error
}

func (e *CommandError) Error() string {
	message := fmt.Sprintf("run tk command %q", e.Command)
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	if e.Stderr != "" {
		message += ": " + e.Stderr
	}
	return message
}

func (e *CommandError) Unwrap() error { return e.Err }

// ResponseError means tk returned output that was not valid JSON or did not
// satisfy the published schema. No decoded value is exposed for this result.
type ResponseError struct {
	Command  string
	Problems []string
	Output   []byte
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("tk command %q returned an invalid response: %s", e.Command, strings.Join(e.Problems, "; "))
}
